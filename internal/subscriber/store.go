package subscriber

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/maeilham/server/internal/pkg/closeutil"
)

type Subscriber struct {
	ID    int64
	Email string
}

type Subscription struct {
	RepoSlug string
	Weight   int
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Upsert inserts a new subscriber or does nothing if already exists.
// Returns the subscriber's ID either way.
func (s *Store) Upsert(ctx context.Context, email string) (int64, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subscribers (email) VALUES (?) ON CONFLICT(email) DO NOTHING`,
		email,
	)
	if err != nil {
		return 0, fmt.Errorf("upsert subscriber: %w", err)
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM subscribers WHERE email = ?`, email).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get subscriber id: %w", err)
	}
	return id, nil
}

// Confirm sets confirmed_at and creates subscriptions.
// repoWeights maps repo slug to weight. If empty, subscribes to all active repos with default weight 3.
func (s *Store) Confirm(ctx context.Context, email string, repoWeights map[string]int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`UPDATE subscribers SET confirmed_at = ? WHERE email = ? AND confirmed_at IS NULL`,
		time.Now().UTC(), email,
	)
	if err != nil {
		return fmt.Errorf("confirm subscriber: %w", err)
	}

	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM subscribers WHERE email = ?`, email).Scan(&id); err != nil {
		return fmt.Errorf("get subscriber: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM subscriptions WHERE subscriber_id = ?`, id); err != nil {
		return fmt.Errorf("clear subscriptions: %w", err)
	}

	if len(repoWeights) > 0 {
		for slug, weight := range repoWeights {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO subscriptions (subscriber_id, repo_slug, weight) VALUES (?, ?, ?)`,
				id, slug, weight,
			); err != nil {
				return fmt.Errorf("create subscription for %s: %w", slug, err)
			}
		}
	} else {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO subscriptions (subscriber_id, repo_slug, weight)
			SELECT ?, slug, 3 FROM repos WHERE active = 1
		`, id)
		if err != nil {
			return fmt.Errorf("create subscriptions: %w", err)
		}
	}

	return tx.Commit()
}

// Reactivate clears unsubscribed_at so the email can re-subscribe.
func (s *Store) Reactivate(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE subscribers SET unsubscribed_at = NULL, confirmed_at = NULL WHERE email = ? AND unsubscribed_at IS NOT NULL`,
		email,
	)
	return err
}

// Unsubscribe sets unsubscribed_at.
func (s *Store) Unsubscribe(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE subscribers SET unsubscribed_at = ? WHERE email = ? AND unsubscribed_at IS NULL`,
		time.Now().UTC(), email,
	)
	return err
}

// ListActive returns all confirmed, non-paused, non-unsubscribed subscribers.
func (s *Store) ListActive(ctx context.Context) ([]Subscriber, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, email FROM subscribers
		 WHERE confirmed_at IS NOT NULL
		   AND paused_at IS NULL
		   AND unsubscribed_at IS NULL
		 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer closeutil.Discard(rows)
	var out []Subscriber
	for rows.Next() {
		var s Subscriber
		if err := rows.Scan(&s.ID, &s.Email); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// IsActive reports whether the subscriber is confirmed and not paused/unsubscribed.
func (s *Store) IsActive(ctx context.Context, id int64) (bool, error) {
	var confirmed, paused, unsubscribed sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT confirmed_at, paused_at, unsubscribed_at FROM subscribers WHERE id = ?`, id,
	).Scan(&confirmed, &paused, &unsubscribed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load subscriber %d: %w", id, err)
	}
	return confirmed.Valid && !paused.Valid && !unsubscribed.Valid, nil
}

// LoadSubscriptions returns active subscriptions for a subscriber.
func (s *Store) LoadSubscriptions(ctx context.Context, id int64) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.repo_slug, s.weight
		  FROM subscriptions s
		  JOIN repos r ON r.slug = s.repo_slug
		 WHERE s.subscriber_id = ? AND r.active = 1
		 ORDER BY s.repo_slug`, id)
	if err != nil {
		return nil, fmt.Errorf("load subscriptions: %w", err)
	}
	defer closeutil.Discard(rows)
	var out []Subscription
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.RepoSlug, &sub.Weight); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// IsUnsubscribed reports whether the email has unsubscribed.
func (s *Store) IsUnsubscribed(ctx context.Context, email string) (bool, error) {
	var t sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT unsubscribed_at FROM subscribers WHERE email = ?`, email).Scan(&t)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return t.Valid, nil
}
