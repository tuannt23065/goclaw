package news

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Dispatcher sends news items to the goctech team leader via the message bus.
// Leader receives a fresh-session message per item and orchestrates
// writer → artist → poster as it would for a cron-triggered run.
type Dispatcher struct {
	msgBus       *bus.MessageBus
	teamStore    store.TeamStore
	agentStore   store.AgentStore
	tenantID     uuid.UUID
	userID       string
	teamID       uuid.UUID
	leaderAgent  string // leader AGENT KEY (e.g. "goctech-leader")
}

type DispatcherConfig struct {
	TenantID    uuid.UUID
	UserID      string
	TeamID      uuid.UUID
	LeaderAgent string // agent_key
}

func NewDispatcher(msgBus *bus.MessageBus, teamStore store.TeamStore, agentStore store.AgentStore, cfg DispatcherConfig) *Dispatcher {
	return &Dispatcher{
		msgBus:      msgBus,
		teamStore:   teamStore,
		agentStore:  agentStore,
		tenantID:    cfg.TenantID,
		userID:      cfg.UserID,
		teamID:      cfg.TeamID,
		leaderAgent: cfg.LeaderAgent,
	}
}

// Dispatch publishes an inbound message to the leader with SOURCE_URL_HINT.
// Returns a fake task ID (UUID derived from item.ID) used for DB tracking;
// the real team_task is created by the leader during its pipeline.
func (d *Dispatcher) Dispatch(ctx context.Context, item FeedItem, source FeedSubscription) (uuid.UUID, error) {
	content := fmt.Sprintf(`[NEWS_MONITOR — Breaking news trigger]

SOURCE_URL_HINT: %s
SOURCE_NAME: %s
TITLE: %s

Triển khai pipeline đăng bài GócTech như bình thường:
1. Dispatch đúng 1 task writer với SOURCE_URL_HINT: %s (để writer viết bài từ URL này, SKIP bước tìm tin).
2. Sau khi writer xong → dispatch artist → poster.

Hard rule: 1 bài = 1 writer + 1 artist + 1 poster. Dùng đúng flow hiện tại của team.`,
		item.URL, source.SourceName, item.Title, item.URL,
	)

	// Use per-item chat ID so the leader session is fresh each time (no carry-over
	// context between news items). SessionKey will be
	// agent:goctech-leader:news:direct:<itemID>.
	chatID := item.ID.String()

	meta := map[string]string{
		"origin_channel":   "news",
		"origin_peer_kind": "direct",
		"source_url":       item.URL,
		"source_name":      source.SourceName,
		"feed_item_id":     item.ID.String(),
		"team_id":          d.teamID.String(),
	}

	ctx = store.WithTenantID(ctx, d.tenantID)

	d.msgBus.PublishInbound(bus.InboundMessage{
		Channel:  "news",
		SenderID: "news-monitor",
		ChatID:   chatID,
		Content:  content,
		UserID:   d.userID,
		AgentID:  d.leaderAgent,
		TenantID: d.tenantID,
		Metadata: meta,
	})

	slog.Info("news.dispatch.sent", "item_id", item.ID, "url", item.URL, "title", item.Title, "leader", d.leaderAgent)

	// Return item.ID as the tracking token (no actual team_task ID yet —
	// leader creates those inside its pipeline).
	return item.ID, nil
}
