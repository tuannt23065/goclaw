package methods

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/news"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// NewsMethods exposes admin RPCs for the news monitor: list/manage feeds,
// browse fetched items, view monitor status, and trigger an out-of-band cycle.
//
// Tenant scope: every read/write filters by the caller's tenant_id. The
// monitor itself runs under cfg.NewsMonitor.TenantID; if a caller from a
// different tenant manages feeds, the monitor will not pick them up until a
// per-tenant monitor instance is added (Phase 3.5).
type NewsMethods struct {
	store   *news.Store
	monitor *news.Monitor // nil-safe; status/run methods degrade gracefully
	cfg     *config.Config

	// Manual-trigger rate limit: 1 trigger per minute per tenant.
	triggerMu       sync.Mutex
	lastTriggerAt   map[string]time.Time
	triggerInterval time.Duration
}

func NewNewsMethods(s *news.Store, m *news.Monitor, cfg *config.Config) *NewsMethods {
	return &NewsMethods{
		store:           s,
		monitor:         m,
		cfg:             cfg,
		lastTriggerAt:   make(map[string]time.Time),
		triggerInterval: time.Minute,
	}
}

func (h *NewsMethods) Register(router *gateway.MethodRouter) {
	if h.store == nil {
		// News monitor was not wired (DB unavailable / opt-out). Skip silently.
		return
	}
	router.Register(protocol.MethodNewsFeedsList, h.requireTenantAdmin(h.handleFeedsList))
	router.Register(protocol.MethodNewsFeedsCreate, h.requireTenantAdmin(h.handleFeedsCreate))
	router.Register(protocol.MethodNewsFeedsUpdate, h.requireTenantAdmin(h.handleFeedsUpdate))
	router.Register(protocol.MethodNewsFeedsDelete, h.requireTenantAdmin(h.handleFeedsDelete))
	router.Register(protocol.MethodNewsFeedsToggle, h.requireTenantAdmin(h.handleFeedsToggle))
	router.Register(protocol.MethodNewsItemsList, h.requireTenantAdmin(h.handleItemsList))
	router.Register(protocol.MethodNewsMonitorStatus, h.requireTenantAdmin(h.handleMonitorStatus))
	router.Register(protocol.MethodNewsMonitorRun, h.requireTenantAdmin(h.handleMonitorRun))
}

// requireTenantAdmin requires either owner role (system bypass) or tenant
// admin within the caller's tenant. Mirrors http.requireTenantAdmin pattern.
func (h *NewsMethods) requireTenantAdmin(next gateway.MethodHandler) gateway.MethodHandler {
	return func(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
		if !permissions.HasMinRole(client.Role(), permissions.RoleAdmin) {
			locale := store.LocaleFromContext(ctx)
			client.SendResponse(protocol.NewErrorResponse(
				req.ID, protocol.ErrUnauthorized,
				i18n.T(locale, i18n.MsgPermissionDenied, req.Method)))
			return
		}
		next(ctx, client, req)
	}
}

// tenantFromCtx returns the caller's tenant uuid, defaulting to master when
// the request has no scope (system owner sessions).
func tenantFromCtx(ctx context.Context) uuid.UUID {
	if t := store.TenantIDFromContext(ctx); t != uuid.Nil {
		return t
	}
	// Master fallback for system owner sessions that haven't picked a scope.
	return uuid.MustParse("0193a5b0-7000-7000-8000-000000000001")
}

// -----------------------------------------------------------------------------
// news.feeds.list
// -----------------------------------------------------------------------------

func (h *NewsMethods) handleFeedsList(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	feeds, err := h.store.AdminListFeeds(ctx, tenantFromCtx(ctx))
	if err != nil {
		respondInternal(client, req, ctx, "list feeds", err)
		return
	}
	out := make([]map[string]any, 0, len(feeds))
	for _, f := range feeds {
		out = append(out, feedToMap(f))
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"feeds": out}))
}

// -----------------------------------------------------------------------------
// news.feeds.create
// -----------------------------------------------------------------------------

func (h *NewsMethods) handleFeedsCreate(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var p struct {
		URL        string `json:"url"`
		SourceName string `json:"sourceName"`
		SourceType string `json:"sourceType"`
		Category   string `json:"category"`
		Priority   int    `json:"priority"`
		Active     *bool  `json:"active"`
	}
	if !parseParams(client, req, ctx, &p) {
		return
	}
	if strings.TrimSpace(p.URL) == "" || strings.TrimSpace(p.SourceName) == "" {
		respondInvalid(client, req, ctx, "url and sourceName are required")
		return
	}
	if !validSourceType(p.SourceType) {
		respondInvalid(client, req, ctx, "sourceType must be one of: rss, atom, html, search")
		return
	}
	if p.Priority < 1 || p.Priority > 10 {
		p.Priority = 5
	}
	active := true
	if p.Active != nil {
		active = *p.Active
	}
	id, err := h.store.AdminCreateFeed(ctx, tenantFromCtx(ctx), news.FeedSubscription{
		URL:        strings.TrimSpace(p.URL),
		SourceName: strings.TrimSpace(p.SourceName),
		SourceType: news.SourceType(p.SourceType),
		Category:   strings.TrimSpace(p.Category),
		Priority:   p.Priority,
		Active:     active,
	})
	if err != nil {
		// Friendly message for duplicate URL.
		if strings.Contains(err.Error(), "tenant_url") || strings.Contains(err.Error(), "duplicate") {
			respondInvalid(client, req, ctx, "feed with this URL already exists for your tenant")
			return
		}
		respondInternal(client, req, ctx, "create feed", err)
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"id": id.String()}))
}

// -----------------------------------------------------------------------------
// news.feeds.update
// -----------------------------------------------------------------------------

func (h *NewsMethods) handleFeedsUpdate(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var p struct {
		ID         string  `json:"id"`
		URL        *string `json:"url"`
		SourceName *string `json:"sourceName"`
		SourceType *string `json:"sourceType"`
		Category   *string `json:"category"`
		Priority   *int    `json:"priority"`
		Active     *bool   `json:"active"`
	}
	if !parseParams(client, req, ctx, &p) {
		return
	}
	feedID, ok := parseUUIDParam(client, req, ctx, "id", p.ID)
	if !ok {
		return
	}
	if p.SourceType != nil && !validSourceType(*p.SourceType) {
		respondInvalid(client, req, ctx, "sourceType must be one of: rss, atom, html, search")
		return
	}
	updates := news.AdminFeedUpdate{
		URL: p.URL, SourceName: p.SourceName, SourceType: p.SourceType,
		Category: p.Category, Priority: p.Priority, Active: p.Active,
	}
	if err := h.store.AdminUpdateFeed(ctx, tenantFromCtx(ctx), feedID, updates); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondNotFound(client, req, ctx, "feed not found in your tenant")
			return
		}
		respondInternal(client, req, ctx, "update feed", err)
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"ok": true}))
}

// -----------------------------------------------------------------------------
// news.feeds.delete
// -----------------------------------------------------------------------------

func (h *NewsMethods) handleFeedsDelete(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var p struct {
		ID string `json:"id"`
	}
	if !parseParams(client, req, ctx, &p) {
		return
	}
	feedID, ok := parseUUIDParam(client, req, ctx, "id", p.ID)
	if !ok {
		return
	}
	if err := h.store.AdminDeleteFeed(ctx, tenantFromCtx(ctx), feedID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondNotFound(client, req, ctx, "feed not found in your tenant")
			return
		}
		respondInternal(client, req, ctx, "delete feed", err)
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"ok": true}))
}

// -----------------------------------------------------------------------------
// news.feeds.toggle
// -----------------------------------------------------------------------------

func (h *NewsMethods) handleFeedsToggle(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var p struct {
		ID     string `json:"id"`
		Active bool   `json:"active"`
	}
	if !parseParams(client, req, ctx, &p) {
		return
	}
	feedID, ok := parseUUIDParam(client, req, ctx, "id", p.ID)
	if !ok {
		return
	}
	if err := h.store.AdminUpdateFeed(ctx, tenantFromCtx(ctx), feedID,
		news.AdminFeedUpdate{Active: &p.Active}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondNotFound(client, req, ctx, "feed not found in your tenant")
			return
		}
		respondInternal(client, req, ctx, "toggle feed", err)
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"ok": true, "active": p.Active}))
}

// -----------------------------------------------------------------------------
// news.items.list
// -----------------------------------------------------------------------------

func (h *NewsMethods) handleItemsList(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var p struct {
		Status   string `json:"status"`
		FeedID   string `json:"feedId"`
		SinceMin int    `json:"sinceMin"` // last N minutes; 0 = no time filter
		Limit    int    `json:"limit"`
	}
	if !parseParams(client, req, ctx, &p) {
		return
	}
	filter := news.AdminItemFilter{
		Status: news.ItemStatus(strings.TrimSpace(p.Status)),
		Limit:  p.Limit,
	}
	if p.FeedID != "" {
		fid, ok := parseUUIDParam(client, req, ctx, "feedId", p.FeedID)
		if !ok {
			return
		}
		filter.FeedID = fid
	}
	if p.SinceMin > 0 {
		t := time.Now().Add(-time.Duration(p.SinceMin) * time.Minute)
		filter.Since = &t
	}
	items, err := h.store.AdminListItems(ctx, tenantFromCtx(ctx), filter)
	if err != nil {
		respondInternal(client, req, ctx, "list items", err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, itemToMap(it))
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"items": out}))
}

// -----------------------------------------------------------------------------
// news.monitor.status
// -----------------------------------------------------------------------------

func (h *NewsMethods) handleMonitorStatus(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	tenantID := tenantFromCtx(ctx)
	since := time.Now().Add(-24 * time.Hour)
	stats, err := h.store.AdminCycleStats(ctx, tenantID, since)
	if err != nil {
		respondInternal(client, req, ctx, "cycle stats", err)
		return
	}

	last, _ := h.store.LastDispatchedAt(ctx) // global last; nil-safe

	nm := h.cfg.NewsMonitor
	monitorTenant := strings.TrimSpace(nm.TenantID)
	tenantMatchesMonitor := monitorTenant != "" && monitorTenant == tenantID.String()

	resp := map[string]any{
		"enabled":              nm.Enabled,
		"intervalMinutes":      nm.IntervalMinutes,
		"minDispatchGapMin":    nm.MinDispatchGapMin,
		"quietHoursStart":      nm.QuietHoursStart,
		"quietHoursEnd":        nm.QuietHoursEnd,
		"heuristicThreshold":   nm.HeuristicThreshold,
		"maxDispatchPerCycle":  nm.MaxDispatchPerCycle,
		"monitorTenantId":      monitorTenant,
		"tenantMatchesMonitor": tenantMatchesMonitor,
		"last24h": map[string]any{
			"dispatched":   stats.Dispatched,
			"scored":       stats.Scored,
			"skipped":      stats.Skipped,
			"duplicates":   stats.Duplicates,
			"totalFetched": stats.TotalFetched,
		},
	}
	if last != nil {
		resp["lastDispatchedAt"] = last.UTC().Format(time.RFC3339)
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, resp))
}

// -----------------------------------------------------------------------------
// news.monitor.run — manual cycle trigger, rate-limited 1/min per tenant.
// -----------------------------------------------------------------------------

func (h *NewsMethods) handleMonitorRun(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if h.monitor == nil {
		respondInvalid(client, req, ctx, "news monitor is not enabled on this gateway")
		return
	}
	tenantID := tenantFromCtx(ctx).String()

	h.triggerMu.Lock()
	last, ok := h.lastTriggerAt[tenantID]
	if ok && time.Since(last) < h.triggerInterval {
		retryIn := h.triggerInterval - time.Since(last)
		h.triggerMu.Unlock()
		respondInvalid(client, req, ctx,
			"manual trigger rate-limited; retry in "+retryIn.Round(time.Second).String())
		return
	}
	h.lastTriggerAt[tenantID] = time.Now()
	h.triggerMu.Unlock()

	// Run the cycle in the background so the WS call returns immediately.
	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		runCtx = store.WithTenantID(runCtx, uuid.MustParse(tenantID))
		if err := h.monitor.RunCycle(runCtx); err != nil {
			slog.Warn("news.monitor.run.error", "error", err)
		}
	}()
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"started": true,
		"note":    "cycle running in background; check news.monitor.status for results",
	}))
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func feedToMap(f news.FeedSubscription) map[string]any {
	m := map[string]any{
		"id":             f.ID.String(),
		"url":            f.URL,
		"sourceName":     f.SourceName,
		"sourceType":     string(f.SourceType),
		"category":       f.Category,
		"priority":       f.Priority,
		"active":         f.Active,
		"fetchFailCount": f.FetchFailCount,
	}
	if f.LastPolledAt != nil {
		m["lastPolledAt"] = f.LastPolledAt.UTC().Format(time.RFC3339)
	}
	return m
}

func itemToMap(v news.AdminItemView) map[string]any {
	m := map[string]any{
		"id":         v.ID.String(),
		"feedId":     v.FeedID.String(),
		"sourceName": v.SourceName,
		"url":        v.URL,
		"title":      v.Title,
		"summary":    v.Summary,
		"status":     string(v.Status),
		"fetchedAt":  v.FetchedAt.UTC().Format(time.RFC3339),
	}
	if v.HeuristicScore != nil {
		m["heuristicScore"] = *v.HeuristicScore
	}
	if v.PublishedAt != nil {
		m["publishedAt"] = v.PublishedAt.UTC().Format(time.RFC3339)
	}
	if v.DispatchedAt != nil {
		m["dispatchedAt"] = v.DispatchedAt.UTC().Format(time.RFC3339)
	}
	if v.DispatchedTaskID != nil {
		m["dispatchedTaskId"] = v.DispatchedTaskID.String()
	}
	if v.SkipReason != "" {
		m["skipReason"] = v.SkipReason
	}
	return m
}

func validSourceType(s string) bool {
	switch s {
	case "rss", "atom", "html", "search":
		return true
	}
	return false
}

func parseParams(client *gateway.Client, req *protocol.RequestFrame, ctx context.Context, dst any) bool {
	if len(req.Params) == 0 {
		return true
	}
	if err := json.Unmarshal(req.Params, dst); err != nil {
		respondInvalid(client, req, ctx, "invalid params: "+err.Error())
		return false
	}
	return true
}

func parseUUIDParam(client *gateway.Client, req *protocol.RequestFrame, ctx context.Context, name, raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		respondInvalid(client, req, ctx, name+" must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func respondInvalid(client *gateway.Client, req *protocol.RequestFrame, ctx context.Context, msg string) {
	locale := store.LocaleFromContext(ctx)
	client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest,
		i18n.T(locale, i18n.MsgInvalidRequest, msg)))
}

func respondInternal(client *gateway.Client, req *protocol.RequestFrame, ctx context.Context, op string, err error) {
	locale := store.LocaleFromContext(ctx)
	slog.Warn("news.method.error", "op", op, "error", err)
	client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal,
		i18n.T(locale, i18n.MsgInternalError, op+": "+err.Error())))
}

func respondNotFound(client *gateway.Client, req *protocol.RequestFrame, ctx context.Context, msg string) {
	locale := store.LocaleFromContext(ctx)
	client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound,
		i18n.T(locale, i18n.MsgInvalidRequest, msg)))
}
