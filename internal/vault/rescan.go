package vault

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/eventbus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// RescanParams holds input for workspace rescan.
type RescanParams struct {
	TenantID  string
	AgentID   string
	Workspace string // absolute path to agent's workspace root
}

// RescanResult holds the outcome of a workspace rescan.
type RescanResult struct {
	Scanned   int  `json:"scanned"`
	New       int  `json:"new"`
	Updated   int  `json:"updated"`
	Unchanged int  `json:"unchanged"`
	Skipped   int  `json:"skipped"`
	Errors    int  `json:"errors"`
	Truncated bool `json:"truncated"`
}

// RescanWorkspace walks the agent workspace and registers missing or changed
// files in vault_documents. Publishes EventVaultDocUpserted for each new or
// updated file so the enrichment worker can process them asynchronously.
func RescanWorkspace(ctx context.Context, params RescanParams, vs store.VaultStore, bus eventbus.DomainEventBus) (*RescanResult, error) {
	entries, walkStats, err := SafeWalkWorkspace(ctx, params.Workspace, DefaultWalkOptions())
	if err != nil {
		return nil, err
	}

	result := &RescanResult{
		Scanned:   walkStats.Eligible,
		Skipped:   walkStats.SkippedExcluded + walkStats.SkippedSymlinks + walkStats.SkippedTooLarge,
		Truncated: walkStats.Truncated,
	}

	for _, entry := range entries {
		hash, hashErr := ContentHashFile(entry.AbsPath)
		if hashErr != nil {
			result.Errors++
			continue
		}

		// Check if document already exists with same hash.
		existing, _ := vs.GetDocument(ctx, params.TenantID, params.AgentID, entry.RelPath)
		if existing != nil && existing.ContentHash == hash {
			result.Unchanged++
			continue
		}

		scope, teamID := inferScopeFromPath(entry.RelPath)
		doc := &store.VaultDocument{
			TenantID:    params.TenantID,
			AgentID:     params.AgentID,
			TeamID:      teamID,
			Scope:       scope,
			Path:        entry.RelPath,
			Title:       InferTitle(entry.RelPath),
			DocType:     InferDocType(entry.RelPath),
			ContentHash: hash,
		}

		if err := vs.UpsertDocument(ctx, doc); err != nil {
			slog.Warn("vault.rescan: upsert", "path", entry.RelPath, "err", err)
			result.Errors++
			continue
		}

		if existing != nil {
			result.Updated++
		} else {
			result.New++
		}

		// Publish enrichment event.
		if bus != nil {
			bus.Publish(eventbus.DomainEvent{
				ID:        uuid.Must(uuid.NewV7()).String(),
				Type:      eventbus.EventVaultDocUpserted,
				SourceID:  doc.ID + ":" + hash,
				TenantID:  params.TenantID,
				AgentID:   params.AgentID,
				Timestamp: time.Now(),
				Payload: eventbus.VaultDocUpsertedPayload{
					DocID:       doc.ID,
					TenantID:    params.TenantID,
					AgentID:     params.AgentID,
					Path:        entry.RelPath,
					ContentHash: hash,
					Workspace:   params.Workspace,
				},
			})
		}
	}

	slog.Info("vault.rescan", "agent", params.AgentID,
		"scanned", result.Scanned, "new", result.New,
		"updated", result.Updated, "unchanged", result.Unchanged,
		"errors", result.Errors, "truncated", result.Truncated)

	return result, nil
}

// inferScopeFromPath detects scope and team from workspace-relative path.
// Paths starting with "teams/{id}/" are team-scoped; everything else is personal.
func inferScopeFromPath(relPath string) (scope string, teamID *string) {
	if !strings.HasPrefix(relPath, "teams/") {
		return "personal", nil
	}
	rest := relPath[len("teams/"):]
	id, _, hasSlash := strings.Cut(rest, "/")
	if !hasSlash || id == "" {
		return "personal", nil
	}
	return "team", &id
}

// InferDocType guesses doc_type from path conventions.
// Exported so both rescan and vault interceptor share the same logic.
func InferDocType(relPath string) string {
	lower := strings.ToLower(relPath)
	ext := strings.ToLower(filepath.Ext(relPath))

	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp",
		".mp4", ".webm", ".mov", ".avi", ".mkv",
		".mp3", ".wav", ".ogg", ".flac", ".aac", ".m4a":
		return "media"
	}

	switch {
	case strings.HasPrefix(lower, "memory/"):
		return "memory"
	case strings.Contains(lower, "soul.md") || strings.Contains(lower, "identity.md") || strings.Contains(lower, "agents.md"):
		return "context"
	case strings.HasPrefix(lower, "skills/") || strings.HasSuffix(lower, "skill.md"):
		return "skill"
	case strings.HasPrefix(lower, "episodic/"):
		return "episodic"
	default:
		return "note"
	}
}

// InferTitle extracts a human-readable title from a file path.
// Exported so both rescan and vault interceptor share the same logic.
func InferTitle(relPath string) string {
	base := filepath.Base(relPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
