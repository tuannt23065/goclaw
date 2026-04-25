package news

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Store wraps the news monitor's DB queries. The Store is bound to a single
// tenant — the news monitor is a per-instance singleton and all monitor-side
// queries (fetcher poll updates, dispatcher reads, dedup checks) filter by
// this tenant. Admin-side queries used by the Web UI accept tenantID
// explicitly so a master admin could in principle inspect another tenant.
type Store struct {
	db       *sql.DB
	tenantID uuid.UUID
}

func NewStore(db *sql.DB, tenantID uuid.UUID) *Store {
	return &Store{db: db, tenantID: tenantID}
}

// TenantID returns the tenant the monitor itself runs under.
func (s *Store) TenantID() uuid.UUID { return s.tenantID }

// -----------------------------------------------------------------------------
// Monitor-side queries (filtered by s.tenantID)
// -----------------------------------------------------------------------------

func (s *Store) ListActiveFeeds(ctx context.Context) ([]FeedSubscription, error) {
	const q = `
		SELECT id, url, source_name, source_type, COALESCE(category,'') AS category,
		       priority, active, last_polled_at, COALESCE(last_etag,'') AS last_etag,
		       COALESCE(last_modified,'') AS last_modified, fetch_fail_count
		  FROM news_feed_subscriptions
		 WHERE tenant_id = $1 AND active = true
		 ORDER BY last_polled_at NULLS FIRST, priority DESC
	`
	return s.queryFeeds(ctx, q, s.tenantID)
}

func (s *Store) UpdateFeedAfterPoll(ctx context.Context, feedID uuid.UUID, etag, modified string, failed bool) error {
	if failed {
		_, err := s.db.ExecContext(ctx, `
			UPDATE news_feed_subscriptions
			   SET last_polled_at = now(),
			       fetch_fail_count = fetch_fail_count + 1,
			       updated_at = now()
			 WHERE id = $1`, feedID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE news_feed_subscriptions
		   SET last_polled_at = now(),
		       last_etag = $2,
		       last_modified = $3,
		       fetch_fail_count = 0,
		       updated_at = now()
		 WHERE id = $1`, feedID, nullStr(etag), nullStr(modified))
	return err
}

// InsertItemIfNew inserts under the monitor's tenant. Returns (true, id) if
// newly inserted, (false, existingID) if URL already existed for this tenant.
func (s *Store) InsertItemIfNew(ctx context.Context, feedID uuid.UUID, item FetchedItem, titleHash []byte, initialStatus ItemStatus) (bool, uuid.UUID, error) {
	status := initialStatus
	if status == "" {
		status = StatusNew
	}
	const q = `
		INSERT INTO news_feed_items (feed_id, tenant_id, url, title, summary, published_at, title_hash, status, skip_reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (tenant_id, url) DO NOTHING
		RETURNING id
	`
	skipReason := ""
	if status == StatusSkipped {
		skipReason = "initial search backfill"
	}
	var id uuid.UUID
	err := s.db.QueryRowContext(ctx, q, feedID, s.tenantID, item.URL, item.Title, nullStr(item.Summary),
		item.PublishedAt, titleHash, string(status), nullStr(skipReason)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if err2 := s.db.QueryRowContext(ctx, `SELECT id FROM news_feed_items WHERE tenant_id = $1 AND url = $2`, s.tenantID, item.URL).Scan(&id); err2 != nil {
			return false, uuid.Nil, err2
		}
		return false, id, nil
	}
	if err != nil {
		return false, uuid.Nil, err
	}
	return true, id, nil
}

// ListReadyForDispatch: status='scored' AND heuristic>=threshold within tenant.
func (s *Store) ListReadyForDispatch(ctx context.Context, threshold, limit int) ([]FeedItem, error) {
	const q = `
		SELECT id, feed_id, url, title, COALESCE(summary,'') AS summary,
		       published_at, fetched_at,
		       heuristic_score, llm_score, COALESCE(llm_reason,'') AS llm_reason,
		       status, dispatched_task_id, COALESCE(skip_reason,'') AS skip_reason, title_hash
		  FROM news_feed_items
		 WHERE tenant_id = $1
		   AND status = 'scored'
		   AND heuristic_score >= $2
		 ORDER BY heuristic_score DESC, COALESCE(published_at, fetched_at) DESC
		 LIMIT $3
	`
	return s.queryItems(ctx, q, s.tenantID, threshold, limit)
}

func (s *Store) UpdateItemScore(ctx context.Context, itemID uuid.UUID, heuristicScore int) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE news_feed_items
		   SET heuristic_score = $2, status = 'scored'
		 WHERE id = $1 AND status = 'new'`, itemID, heuristicScore)
	return err
}

func (s *Store) MarkItemSkipped(ctx context.Context, itemID uuid.UUID, reason string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE news_feed_items
		   SET status = 'skipped', skip_reason = $2
		 WHERE id = $1`, itemID, reason)
	return err
}

func (s *Store) MarkItemDuplicate(ctx context.Context, itemID uuid.UUID, reason string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE news_feed_items
		   SET status = 'duplicate', skip_reason = $2
		 WHERE id = $1`, itemID, reason)
	return err
}

func (s *Store) MarkItemDispatched(ctx context.Context, itemID, taskID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE news_feed_items
		   SET status = 'dispatched', dispatched_task_id = $2, dispatched_at = now()
		 WHERE id = $1`, itemID, taskID)
	return err
}

// CountDispatchedSince returns items dispatched within the given duration for
// the monitor's tenant.
func (s *Store) CountDispatchedSince(ctx context.Context, since time.Duration) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM news_feed_items
		 WHERE tenant_id = $1
		   AND status = 'dispatched'
		   AND dispatched_at > now() - $2::interval`,
		s.tenantID, fmt.Sprintf("%d seconds", int(since.Seconds()))).Scan(&count)
	return count, err
}

// LastDispatchedAt returns the most recent dispatch timestamp for the
// monitor's tenant. Reads dispatched_at (set in MarkItemDispatched) — NOT
// fetched_at, which is the ingest time and would let the rate limiter slip
// every cycle when a freshly-dispatched item happened to be fetched several
// cycles ago.
func (s *Store) LastDispatchedAt(ctx context.Context) (*time.Time, error) {
	var ts sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(dispatched_at) FROM news_feed_items
		 WHERE tenant_id = $1 AND status = 'dispatched'`, s.tenantID).Scan(&ts)
	if err != nil {
		return nil, err
	}
	if !ts.Valid {
		return nil, nil
	}
	t := ts.Time
	return &t, nil
}

// HasDuplicateTitleHash returns true if a non-skipped item with the same
// title_hash exists within window for the monitor's tenant.
func (s *Store) HasDuplicateTitleHash(ctx context.Context, itemID uuid.UUID, hash []byte, window time.Duration) (bool, error) {
	if len(hash) == 0 {
		return false, nil
	}
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM news_feed_items
			 WHERE tenant_id = $1
			   AND title_hash = $2
			   AND id != $3
			   AND status IN ('dispatched', 'scored')
			   AND fetched_at > now() - $4::interval
		)`, s.tenantID, hash, itemID, fmt.Sprintf("%d seconds", int(window.Seconds()))).Scan(&exists)
	return exists, err
}

// -----------------------------------------------------------------------------
// Admin queries (Web UI) — explicit tenantID parameter
// -----------------------------------------------------------------------------

// AdminListFeeds returns all feeds for tenant, including inactive.
func (s *Store) AdminListFeeds(ctx context.Context, tenantID uuid.UUID) ([]FeedSubscription, error) {
	const q = `
		SELECT id, url, source_name, source_type, COALESCE(category,'') AS category,
		       priority, active, last_polled_at, COALESCE(last_etag,'') AS last_etag,
		       COALESCE(last_modified,'') AS last_modified, fetch_fail_count
		  FROM news_feed_subscriptions
		 WHERE tenant_id = $1
		 ORDER BY active DESC, source_name
	`
	return s.queryFeeds(ctx, q, tenantID)
}

// AdminCreateFeed inserts a new subscription. Returns the new ID.
func (s *Store) AdminCreateFeed(ctx context.Context, tenantID uuid.UUID, f FeedSubscription) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO news_feed_subscriptions (tenant_id, url, source_name, source_type, category, priority, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		tenantID, f.URL, f.SourceName, string(f.SourceType), nullStr(f.Category), f.Priority, f.Active).Scan(&id)
	return id, err
}

// AdminUpdateFeed updates editable fields. Pass nil for fields you don't want
// to change. Returns sql.ErrNoRows if not found in the tenant.
func (s *Store) AdminUpdateFeed(ctx context.Context, tenantID, feedID uuid.UUID, updates AdminFeedUpdate) error {
	sets := []string{}
	args := []any{tenantID, feedID}
	idx := 3
	if updates.URL != nil {
		sets = append(sets, fmt.Sprintf("url = $%d", idx))
		args = append(args, *updates.URL)
		idx++
	}
	if updates.SourceName != nil {
		sets = append(sets, fmt.Sprintf("source_name = $%d", idx))
		args = append(args, *updates.SourceName)
		idx++
	}
	if updates.SourceType != nil {
		sets = append(sets, fmt.Sprintf("source_type = $%d", idx))
		args = append(args, *updates.SourceType)
		idx++
	}
	if updates.Category != nil {
		sets = append(sets, fmt.Sprintf("category = $%d", idx))
		args = append(args, nullStr(*updates.Category))
		idx++
	}
	if updates.Priority != nil {
		sets = append(sets, fmt.Sprintf("priority = $%d", idx))
		args = append(args, *updates.Priority)
		idx++
	}
	if updates.Active != nil {
		sets = append(sets, fmt.Sprintf("active = $%d", idx))
		args = append(args, *updates.Active)
		idx++
		// Reset fail counter when re-enabling so a failing feed gets one more chance.
		if *updates.Active {
			sets = append(sets, "fetch_fail_count = 0")
		}
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = now()")
	q := fmt.Sprintf(`UPDATE news_feed_subscriptions SET %s WHERE tenant_id = $1 AND id = $2`,
		strings.Join(sets, ", "))
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AdminDeleteFeed removes a subscription and (cascade) all its items.
func (s *Store) AdminDeleteFeed(ctx context.Context, tenantID, feedID uuid.UUID) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM news_feed_subscriptions WHERE tenant_id = $1 AND id = $2`,
		tenantID, feedID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AdminListItems returns items for tenant, optionally filtered by status and
// feed. Summary is truncated to 200 chars to keep payloads small.
func (s *Store) AdminListItems(ctx context.Context, tenantID uuid.UUID, filter AdminItemFilter) ([]AdminItemView, error) {
	conds := []string{"i.tenant_id = $1"}
	args := []any{tenantID}
	idx := 2
	if filter.Status != "" {
		conds = append(conds, fmt.Sprintf("i.status = $%d", idx))
		args = append(args, filter.Status)
		idx++
	}
	if filter.FeedID != uuid.Nil {
		conds = append(conds, fmt.Sprintf("i.feed_id = $%d", idx))
		args = append(args, filter.FeedID)
		idx++
	}
	if filter.Since != nil {
		conds = append(conds, fmt.Sprintf("i.fetched_at > $%d", idx))
		args = append(args, *filter.Since)
		idx++
	}
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit)
	q := fmt.Sprintf(`
		SELECT i.id, i.feed_id, s.source_name, i.url, i.title,
		       COALESCE(LEFT(i.summary, 200),'') AS summary,
		       i.published_at, i.fetched_at, i.dispatched_at,
		       i.heuristic_score, i.status, i.dispatched_task_id,
		       COALESCE(i.skip_reason,'') AS skip_reason
		  FROM news_feed_items i
		  JOIN news_feed_subscriptions s ON s.id = i.feed_id
		 WHERE %s
		 ORDER BY i.fetched_at DESC
		 LIMIT $%d`, strings.Join(conds, " AND "), idx)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AdminItemView
	for rows.Next() {
		var v AdminItemView
		var status string
		if err := rows.Scan(&v.ID, &v.FeedID, &v.SourceName, &v.URL, &v.Title, &v.Summary,
			&v.PublishedAt, &v.FetchedAt, &v.DispatchedAt,
			&v.HeuristicScore, &status, &v.DispatchedTaskID, &v.SkipReason); err != nil {
			return nil, err
		}
		v.Status = ItemStatus(status)
		out = append(out, v)
	}
	return out, rows.Err()
}

// AdminCycleStats returns counts useful for the Settings page.
func (s *Store) AdminCycleStats(ctx context.Context, tenantID uuid.UUID, since time.Time) (AdminCycleStats, error) {
	var st AdminCycleStats
	err := s.db.QueryRowContext(ctx, `
		SELECT
		  COUNT(*) FILTER (WHERE status='dispatched' AND dispatched_at > $2) AS dispatched,
		  COUNT(*) FILTER (WHERE status='scored' AND fetched_at > $2)        AS scored,
		  COUNT(*) FILTER (WHERE status='skipped' AND fetched_at > $2)       AS skipped,
		  COUNT(*) FILTER (WHERE status='duplicate' AND fetched_at > $2)     AS duplicates,
		  COUNT(*) FILTER (WHERE fetched_at > $2)                            AS total_fetched
		  FROM news_feed_items
		 WHERE tenant_id = $1`, tenantID, since).Scan(
		&st.Dispatched, &st.Scored, &st.Skipped, &st.Duplicates, &st.TotalFetched)
	return st, err
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func (s *Store) queryFeeds(ctx context.Context, q string, args ...any) ([]FeedSubscription, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list feeds: %w", err)
	}
	defer rows.Close()

	var out []FeedSubscription
	for rows.Next() {
		var f FeedSubscription
		var srcType string
		if err := rows.Scan(&f.ID, &f.URL, &f.SourceName, &srcType, &f.Category,
			&f.Priority, &f.Active, &f.LastPolledAt, &f.LastETag, &f.LastModified, &f.FetchFailCount); err != nil {
			return nil, err
		}
		f.SourceType = SourceType(srcType)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) queryItems(ctx context.Context, q string, args ...any) ([]FeedItem, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FeedItem
	for rows.Next() {
		var it FeedItem
		var status string
		if err := rows.Scan(&it.ID, &it.FeedID, &it.URL, &it.Title, &it.Summary,
			&it.PublishedAt, &it.FetchedAt,
			&it.HeuristicScore, &it.LLMScore, &it.LLMReason,
			&status, &it.DispatchedTaskID, &it.SkipReason, &it.TitleHash); err != nil {
			return nil, err
		}
		it.Status = ItemStatus(status)
		out = append(out, it)
	}
	return out, rows.Err()
}

// ListItemsByStatus is kept for compatibility with the heuristic scorer's
// existing call sites; it filters by the monitor's tenant.
func (s *Store) ListItemsByStatus(ctx context.Context, status ItemStatus, limit int) ([]FeedItem, error) {
	const q = `
		SELECT id, feed_id, url, title, COALESCE(summary,'') AS summary,
		       published_at, fetched_at,
		       heuristic_score, llm_score, COALESCE(llm_reason,'') AS llm_reason,
		       status, dispatched_task_id, COALESCE(skip_reason,'') AS skip_reason, title_hash
		  FROM news_feed_items
		 WHERE tenant_id = $1 AND status = $2
		 ORDER BY fetched_at DESC
		 LIMIT $3
	`
	return s.queryItems(ctx, q, s.tenantID, string(status), limit)
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
