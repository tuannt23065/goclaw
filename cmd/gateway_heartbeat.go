package cmd

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/agent"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/gateway/methods"
	"github.com/nextlevelbuilder/goclaw/internal/heartbeat"
	"github.com/nextlevelbuilder/goclaw/internal/news"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/scheduler"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// makeHeartbeatRunFn creates a function that routes a heartbeat run through the scheduler's cron lane.
func makeHeartbeatRunFn(sched *scheduler.Scheduler) func(ctx context.Context, req agent.RunRequest) <-chan scheduler.RunOutcome {
	return func(ctx context.Context, req agent.RunRequest) <-chan scheduler.RunOutcome {
		return sched.Schedule(ctx, scheduler.LaneCron, req)
	}
}

// startCronAndHeartbeat starts the cron service and heartbeat ticker, wires the heartbeat
// wake function to the tool + RPC methods, and sets the adaptive token estimate function.
// Returns the heartbeat ticker (needed by lifecycle for shutdown).
func startCronAndHeartbeat(
	pgStores *store.Stores,
	server *gateway.Server,
	sched *scheduler.Scheduler,
	msgBus *bus.MessageBus,
	providerRegistry *providers.Registry,
	channelMgr *channels.Manager,
	cfg *config.Config,
	heartbeatTool *tools.HeartbeatTool,
	heartbeatMethods *methods.HeartbeatMethods,
) *heartbeat.Ticker {
	// Start cron service with job handler (routes through scheduler's cron lane)
	pgStores.Cron.SetOnJob(makeCronJobHandler(sched, msgBus, cfg, channelMgr, pgStores.Sessions, pgStores.Agents))
	pgStores.Cron.SetOnEvent(func(event store.CronEvent) {
		server.BroadcastEvent(*protocol.NewEvent(protocol.EventCron, event))
	})
	if err := pgStores.Cron.Start(); err != nil {
		slog.Warn("cron service failed to start", "error", err)
	}

	// Start heartbeat ticker (routes through scheduler's cron lane)
	heartbeatTicker := heartbeat.NewTicker(heartbeat.TickerConfig{
		Store:         pgStores.Heartbeats,
		Agents:        pgStores.Agents,
		Sessions:      pgStores.Sessions,
		ProviderStore: pgStores.Providers,
		ProviderReg:   providerRegistry,
		MsgBus:        msgBus,
		Sched:         sched,
		RunAgent:      makeHeartbeatRunFn(sched),
	})
	heartbeatTicker.SetOnEvent(func(event store.HeartbeatEvent) {
		server.BroadcastEvent(*protocol.NewEvent(protocol.EventHeartbeat, event))
	})
	heartbeatTicker.Start()

	// Wire heartbeat wake function to tool + RPC + cron wakeMode
	heartbeatTool.SetWakeFn(heartbeatTicker.Wake)
	heartbeatMethods.SetWakeFn(heartbeatTicker.Wake)
	heartbeatMethods.SetAgentStore(pgStores.Agents)
	heartbeatMethods.SetProviderStore(pgStores.Providers)
	cronHeartbeatWakeFn = func(agentID string) {
		if id, err := uuid.Parse(agentID); err == nil {
			heartbeatTicker.Wake(id)
		}
	}

	// Adaptive throttle: reduce per-session concurrency when nearing the summary threshold.
	sched.SetTokenEstimateFunc(func(sessionKey string) (int, int) {
		bctx := context.Background()
		history := pgStores.Sessions.GetHistory(bctx, sessionKey)
		lastPT, lastMC := pgStores.Sessions.GetLastPromptTokens(bctx, sessionKey)
		tokens := agent.EstimateTokensWithCalibration(history, lastPT, lastMC)
		cw := pgStores.Sessions.GetContextWindow(bctx, sessionKey)
		if cw <= 0 {
			cw = config.DefaultContextWindow
		}
		return tokens, cw
	})

	// Start news monitor (event-driven FB posting) if enabled in config.
	newsStore, newsMonitor := startNewsMonitor(context.Background(), pgStores, msgBus, cfg)
	if newsStore != nil {
		methods.NewNewsMethods(newsStore, newsMonitor, cfg).Register(server.Router())
	}

	return heartbeatTicker
}

// startNewsMonitor wires up the RSS/HTML news feed poller that dispatches
// posting tasks to the goctech team leader. Disabled by default — opt-in via
// cfg.NewsMonitor.Enabled. Returns the constructed Store + Monitor so the
// caller can register the admin WS methods (news.feeds.*, news.items.*,
// news.monitor.*); both are nil when the monitor is disabled.
func startNewsMonitor(ctx context.Context, pgStores *store.Stores, msgBus *bus.MessageBus, cfg *config.Config) (*news.Store, *news.Monitor) {
	nm := cfg.NewsMonitor
	if !nm.Enabled {
		slog.Info("news.monitor.disabled")
		return nil, nil
	}
	tenantID, err := uuid.Parse(nm.TenantID)
	if err != nil {
		slog.Warn("news.monitor: invalid tenant_id — monitor not started", "value", nm.TenantID, "error", err)
		return nil, nil
	}
	teamID, err := uuid.Parse(nm.TeamID)
	if err != nil {
		slog.Warn("news.monitor: invalid team_id — monitor not started", "value", nm.TeamID, "error", err)
		return nil, nil
	}
	if nm.LeaderAgentKey == "" || nm.UserID == "" {
		slog.Warn("news.monitor: leader_agent_key or user_id empty — monitor not started")
		return nil, nil
	}
	if pgStores.DB == nil {
		slog.Warn("news.monitor: pgStores.DB nil (SQLite build?) — monitor not started")
		return nil, nil
	}

	interval := nm.IntervalMinutes
	if interval <= 0 {
		interval = 15
	}
	minGap := nm.MinDispatchGapMin
	if minGap <= 0 {
		minGap = 30
	}
	quietStart := nm.QuietHoursStart
	quietEnd := nm.QuietHoursEnd
	if quietEnd == 0 {
		quietEnd = 6
	}
	threshold := nm.HeuristicThreshold
	if threshold <= 0 {
		threshold = 6
	}
	maxPerCycle := nm.MaxDispatchPerCycle
	if maxPerCycle <= 0 {
		maxPerCycle = 2
	}

	newsStore := news.NewStore(pgStores.DB, tenantID)
	fetcher := news.NewFetcher()
	rl := news.NewRateLimiter(newsStore, minGap, quietStart, quietEnd)
	dispatcher := news.NewDispatcher(msgBus, pgStores.Teams, pgStores.Agents, news.DispatcherConfig{
		TenantID:    tenantID,
		UserID:      nm.UserID,
		TeamID:      teamID,
		LeaderAgent: nm.LeaderAgentKey,
	})
	monitor := news.NewMonitor(newsStore, fetcher, rl, dispatcher, news.MonitorConfig{
		HeuristicThreshold:  threshold,
		MaxDispatchPerCycle: maxPerCycle,
	})
	ticker := news.NewTicker(monitor, time.Duration(interval)*time.Minute)
	ticker.Start(ctx)
	slog.Info("news.monitor.started", "interval_min", interval, "team", nm.TeamID, "leader", nm.LeaderAgentKey,
		"min_gap_min", minGap, "quiet", quietStart, "to", quietEnd, "threshold", threshold)
	return newsStore, monitor
}
