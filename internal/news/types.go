// Package news implements an RSS/HTML news feed monitor that dispatches
// Facebook posting tasks to the goctech team leader when fresh, relevant
// tech news appears. Replaces the prior 6-fixed-slot cron schedule with
// event-driven posting.
package news

import (
	"time"

	"github.com/google/uuid"
)

type SourceType string

const (
	SourceRSS    SourceType = "rss"
	SourceAtom   SourceType = "atom"
	SourceHTML   SourceType = "html"
	SourceSearch SourceType = "search" // claude-cli web_search for CF-gated sites
)

type ItemStatus string

const (
	StatusNew        ItemStatus = "new"
	StatusScored     ItemStatus = "scored"
	StatusDispatched ItemStatus = "dispatched"
	StatusSkipped    ItemStatus = "skipped"
	StatusDuplicate  ItemStatus = "duplicate"
	StatusError      ItemStatus = "error"
)

type FeedSubscription struct {
	ID             uuid.UUID
	URL            string
	SourceName     string
	SourceType     SourceType
	Category       string
	Priority       int
	Active         bool
	LastPolledAt   *time.Time
	LastETag       string
	LastModified   string
	FetchFailCount int
}

type FeedItem struct {
	ID               uuid.UUID
	FeedID           uuid.UUID
	URL              string
	Title            string
	Summary          string
	PublishedAt      *time.Time
	FetchedAt        time.Time
	HeuristicScore   *int
	LLMScore         *int
	LLMReason        string
	Status           ItemStatus
	DispatchedTaskID *uuid.UUID
	SkipReason       string
	TitleHash        []byte
}

type FetchResult struct {
	Items   []FetchedItem
	ETag    string
	Modified string
	NotModified bool
	Err     error
}

type FetchedItem struct {
	URL         string
	Title       string
	Summary     string
	PublishedAt *time.Time
}
