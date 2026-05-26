package delivery

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AlreadyDeliveredToday returns true if the subscriber has any delivery_log row
// whose sent_at falls on the same calendar day as `day` (in UTC).
//
// Used by the daily send orchestration to make retries idempotent: if a previous
// run already sent today's mail to this user, we skip them on retry.
func AlreadyDeliveredToday(ctx context.Context, db *sql.DB, subscriberID int64, day time.Time) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx, `
		SELECT 1 FROM delivery_log
		 WHERE subscriber_id = ?
		   AND date(sent_at) = date(?)
		 LIMIT 1`,
		subscriberID, day.Format("2006-01-02"),
	).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check delivery for %d: %w", subscriberID, err)
	}
	return true, nil
}

// RecordDelivery inserts a delivery_log row for (subscriber, content).
// `channel` is typically "email"; reserved for future channels (rss, slack, ...).
// sent_at is set explicitly so that AlreadyDeliveredToday / AdvanceRotation
// (which compare against the orchestrator's `day`) match up — relying on
// CURRENT_TIMESTAMP would drift across timezones or backfilled runs.
func RecordDelivery(ctx context.Context, db *sql.DB, subscriberID int64, c *Content, channel string, sentAt time.Time) error {
	if channel == "" {
		channel = "email"
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO delivery_log(subscriber_id, repo_slug, content_id, channel, sent_at)
		VALUES (?, ?, ?, ?, ?)`,
		subscriberID, c.RepoSlug, c.ContentID, channel, sentAt.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return fmt.Errorf("insert delivery_log: %w", err)
	}
	return nil
}
