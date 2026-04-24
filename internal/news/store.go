package news

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) ListActiveFeeds(ctx context.Context) ([]FeedSubscription, error) {
	const q = `
		SELECT id, url, source_name, source_type, COALESCE(category,'') AS category,
		       priority, active, last_polled_at, COALESCE(last_etag,'') AS last_etag,
		       COALESCE(last_modified,'') AS last_modified, fetch_fail_count
		  FROM news_feed_subscriptions
		 WHERE active = true
		 ORDER BY last_polled_at NULLS FIRST, priority DESC
	`
	rows, err := s.db.QueryContext(ctx, q)
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

// InsertItemIfNew inserts, returns (true, id) if newly inserted, (false, existingID) if URL already existed.
// If initialStatus is empty, defaults to 'new'. Pass 'skipped' for first-poll backfill.
func (s *Store) InsertItemIfNew(ctx context.Context, feedID uuid.UUID, item FetchedItem, titleHash []byte, initialStatus ItemStatus) (bool, uuid.UUID, error) {
	status := initialStatus
	if status == "" {
		status = StatusNew
	}
	const q = `
		INSERT INTO news_feed_items (feed_id, url, title, summary, published_at, title_hash, status, skip_reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (url) DO NOTHING
		RETURNING id
	`
	skipReason := ""
	if status == StatusSkipped {
		skipReason = "initial search backfill"
	}
	var id uuid.UUID
	err := s.db.QueryRowContext(ctx, q, feedID, item.URL, item.Title, nullStr(item.Summary),
		item.PublishedAt, titleHash, string(status), nullStr(skipReason)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// Already exists — fetch existing ID
		if err2 := s.db.QueryRowContext(ctx, `SELECT id FROM news_feed_items WHERE url = $1`, item.URL).Scan(&id); err2 != nil {
			return false, uuid.Nil, err2
		}
		return false, id, nil
	}
	if err != nil {
		return false, uuid.Nil, err
	}
	return true, id, nil
}

func (s *Store) ListItemsByStatus(ctx context.Context, status ItemStatus, limit int) ([]FeedItem, error) {
	const q = `
		SELECT id, feed_id, url, title, COALESCE(summary,'') AS summary,
		       published_at, fetched_at,
		       heuristic_score, llm_score, COALESCE(llm_reason,'') AS llm_reason,
		       status, dispatched_task_id, COALESCE(skip_reason,'') AS skip_reason, title_hash
		  FROM news_feed_items
		 WHERE status = $1
		 ORDER BY fetched_at DESC
		 LIMIT $2
	`
	rows, err := s.db.QueryContext(ctx, q, string(status), limit)
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

// ListReadyForDispatch: status='scored' AND heuristic>=threshold, ordered by score DESC + recency.
func (s *Store) ListReadyForDispatch(ctx context.Context, threshold, limit int) ([]FeedItem, error) {
	const q = `
		SELECT id, feed_id, url, title, COALESCE(summary,'') AS summary,
		       published_at, fetched_at,
		       heuristic_score, llm_score, COALESCE(llm_reason,'') AS llm_reason,
		       status, dispatched_task_id, COALESCE(skip_reason,'') AS skip_reason, title_hash
		  FROM news_feed_items
		 WHERE status = 'scored'
		   AND heuristic_score >= $1
		 ORDER BY heuristic_score DESC, COALESCE(published_at, fetched_at) DESC
		 LIMIT $2
	`
	rows, err := s.db.QueryContext(ctx, q, threshold, limit)
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
		   SET status = 'dispatched', dispatched_task_id = $2
		 WHERE id = $1`, itemID, taskID)
	return err
}

// CountDispatchedSince returns items dispatched within the given duration.
func (s *Store) CountDispatchedSince(ctx context.Context, since time.Duration) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM news_feed_items
		 WHERE status = 'dispatched'
		   AND fetched_at > now() - $1::interval`, fmt.Sprintf("%d seconds", int(since.Seconds()))).Scan(&count)
	return count, err
}

// LastDispatchedAt returns the most recent dispatch timestamp.
func (s *Store) LastDispatchedAt(ctx context.Context) (*time.Time, error) {
	var ts sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(fetched_at) FROM news_feed_items WHERE status = 'dispatched'`).Scan(&ts)
	if err != nil {
		return nil, err
	}
	if !ts.Valid {
		return nil, nil
	}
	t := ts.Time
	return &t, nil
}

// HasDuplicateTitleHash returns true if a non-skipped item with same title_hash exists within window.
func (s *Store) HasDuplicateTitleHash(ctx context.Context, itemID uuid.UUID, hash []byte, window time.Duration) (bool, error) {
	if len(hash) == 0 {
		return false, nil
	}
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM news_feed_items
			 WHERE title_hash = $1
			   AND id != $2
			   AND status IN ('dispatched', 'scored')
			   AND fetched_at > now() - $3::interval
		)`, hash, itemID, fmt.Sprintf("%d seconds", int(window.Seconds()))).Scan(&exists)
	return exists, err
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
