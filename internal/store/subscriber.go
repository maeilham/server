package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/maeilham/server/internal/pkg/closeutil"
	"github.com/maeilham/server/internal/pkg/token"
)

// ErrSubscriberNotFound는 개인 링크 토큰이 어떤 구독자와도 맞지 않을 때 쓴다.
// 토큰이 아예 없는 경우와 해지한 구독자의 토큰인 경우를 이 하나로 합쳐서, 호출자가 둘을
// 구분하지 못하게 한다(401 응답에 "해지한 사람인지"가 새지 않게 하려는 의도).
var ErrSubscriberNotFound = errors.New("subscriber not found")

type Subscriber struct {
	ID    int64
	Email string
}

// SubscriberSession은 개인 링크 토큰 하나가 가리키는 구독자에 대해 세션 API가 알아야 하는 것만 담는다.
type SubscriberSession struct {
	ID        int64
	Confirmed bool
}

type Subscription struct {
	RepoSlug string
	Weight   int
}

// SubscriberRepository는 subscribers/subscriptions 테이블 단일 쿼리 메서드와
// 여러 쿼리를 하나의 트랜잭션으로 묶는 WithTx를 제공한다.
type SubscriberRepository interface {
	// WithTx runs fn within a transaction. Nested calls reuse the existing transaction.
	WithTx(ctx context.Context, fn func(SubscriberRepository) error) error

	Upsert(ctx context.Context, email string) (int64, error)
	// EnsureAccessToken returns the subscriber's personal-link token, creating it if none exists yet.
	// 이미 있으면 그대로 돌려주므로 여러 번 불러도, 동시에 불러도 같은 값이다(링크를 다시 보내도 즐겨찾기가 안 깨진다).
	EnsureAccessToken(ctx context.Context, id int64) (string, error)
	// SubscriberByAccessToken은 tok으로 구독자를 찾는다. 없거나 해지한 사람이면 ErrSubscriberNotFound.
	SubscriberByAccessToken(ctx context.Context, tok string) (SubscriberSession, error)
	// ConfirmByAccessToken은 tok 주인이 미확인이면 confirmed_at을 채운다. 이미 확인된 사람이면
	// (id, false, nil)로 아무것도 바꾸지 않는다(멱등). 토큰이 없거나 해지 상태면 ErrSubscriberNotFound.
	ConfirmByAccessToken(ctx context.Context, tok string) (id int64, wasNewlyConfirmed bool, err error)
	SetConfirmed(ctx context.Context, email string) (int64, error)
	ClearSubscriptions(ctx context.Context, id int64) error
	AddSubscription(ctx context.Context, id int64, slug string, weight int) error
	AddAllActiveRepoSubscriptions(ctx context.Context, id int64) error
	Reactivate(ctx context.Context, email string) error
	Unsubscribe(ctx context.Context, email string) error
	IsUnsubscribed(ctx context.Context, email string) (bool, error)
	ListActive(ctx context.Context) ([]Subscriber, error)
	IsActive(ctx context.Context, id int64) (bool, error)
	LoadSubscriptions(ctx context.Context, id int64) ([]Subscription, error)
}

// subQueries holds all single-query method implementations, shared by both store types.
type subQueries struct{ db dbtx }

// sqlSubscriberStore uses *sql.DB and can start new transactions via WithTx.
type sqlSubscriberStore struct {
	subQueries
	sqlDB *sql.DB
}

// txSubscriberStore uses *sql.Tx; WithTx reuses the existing transaction.
type txSubscriberStore struct {
	subQueries
}

func NewSubscriberStore(db *sql.DB) SubscriberRepository {
	return &sqlSubscriberStore{subQueries: subQueries{db: db}, sqlDB: db}
}

func (s *sqlSubscriberStore) WithTx(ctx context.Context, fn func(SubscriberRepository) error) error {
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := fn(&txSubscriberStore{subQueries: subQueries{db: tx}}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *txSubscriberStore) WithTx(_ context.Context, fn func(SubscriberRepository) error) error {
	return fn(s) // already in a transaction; reuse it
}

// ── Single-query methods on subQueries ───────────────────────────────────────

func (s *subQueries) Upsert(ctx context.Context, email string) (int64, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subscribers (email) VALUES (?) ON CONFLICT(email) DO NOTHING`, email,
	)
	if err != nil {
		return 0, fmt.Errorf("upsert subscriber: %w", err)
	}
	var id int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM subscribers WHERE email = ?`, email,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("get subscriber id: %w", err)
	}
	return id, nil
}

func (s *subQueries) EnsureAccessToken(ctx context.Context, id int64) (string, error) {
	tok, err := token.NewAccessToken()
	if err != nil {
		return "", fmt.Errorf("generate access token: %w", err)
	}
	// 비어 있는 경우에만 채운다. 동시에 두 요청이 와도 하나만 이기고, 진 쪽은 아래에서 이긴 값을 읽는다.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE subscribers SET access_token = ? WHERE id = ? AND access_token IS NULL`, tok, id,
	); err != nil {
		return "", fmt.Errorf("set access token: %w", err)
	}
	var got sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT access_token FROM subscribers WHERE id = ?`, id,
	).Scan(&got); err != nil {
		return "", fmt.Errorf("get access token for subscriber %d: %w", id, err)
	}
	if !got.Valid {
		return "", fmt.Errorf("subscriber %d has no access token", id)
	}
	return got.String, nil
}

// SubscriberByAccessToken은 paused_at을 보지 않는다. 일시정지는 발송 대상 선정(ListActive/IsActive)에만
// 쓰는 값이고, 정지된 사람도 자기 링크로 들어와 화면을 보는 것까지 막을 이유는 없다는 가정이다.
// 나중에 다르게 정해지면 이 함수에 paused 체크만 추가하면 된다.
func (s *subQueries) SubscriberByAccessToken(ctx context.Context, tok string) (SubscriberSession, error) {
	var id int64
	var confirmed, unsubscribed sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT id, confirmed_at, unsubscribed_at FROM subscribers WHERE access_token = ?`, tok,
	).Scan(&id, &confirmed, &unsubscribed)
	if err == sql.ErrNoRows {
		return SubscriberSession{}, ErrSubscriberNotFound
	}
	if err != nil {
		return SubscriberSession{}, fmt.Errorf("find subscriber by access token: %w", err)
	}
	if unsubscribed.Valid {
		return SubscriberSession{}, ErrSubscriberNotFound
	}
	return SubscriberSession{ID: id, Confirmed: confirmed.Valid}, nil
}

func (s *subQueries) ConfirmByAccessToken(ctx context.Context, tok string) (int64, bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE subscribers SET confirmed_at = ?
		  WHERE access_token = ? AND confirmed_at IS NULL AND unsubscribed_at IS NULL`,
		time.Now().UTC(), tok,
	)
	if err != nil {
		return 0, false, fmt.Errorf("confirm by access token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("confirm by access token: %w", err)
	}
	if n == 0 {
		// 이미 확인됐거나, 토큰이 없거나, 해지 상태다. 어느 쪽인지는 같은 조회로 가려낸다.
		sess, err := s.SubscriberByAccessToken(ctx, tok)
		if err != nil {
			return 0, false, err
		}
		return sess.ID, false, nil
	}
	var id int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM subscribers WHERE access_token = ?`, tok,
	).Scan(&id); err != nil {
		return 0, false, fmt.Errorf("get subscriber id for access token: %w", err)
	}
	return id, true, nil
}

func (s *subQueries) SetConfirmed(ctx context.Context, email string) (int64, error) {
	_, err := s.db.ExecContext(ctx,
		`UPDATE subscribers SET confirmed_at = ? WHERE email = ? AND confirmed_at IS NULL`,
		time.Now().UTC(), email,
	)
	if err != nil {
		return 0, fmt.Errorf("set confirmed_at: %w", err)
	}
	var id int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM subscribers WHERE email = ?`, email,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("get subscriber id: %w", err)
	}
	return id, nil
}

func (s *subQueries) ClearSubscriptions(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM subscriptions WHERE subscriber_id = ?`, id)
	if err != nil {
		return fmt.Errorf("clear subscriptions: %w", err)
	}
	return nil
}

func (s *subQueries) AddSubscription(ctx context.Context, id int64, slug string, weight int) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subscriptions (subscriber_id, repo_slug, weight) VALUES (?, ?, ?)`,
		id, slug, weight,
	)
	if err != nil {
		return fmt.Errorf("add subscription %s: %w", slug, err)
	}
	return nil
}

func (s *subQueries) AddAllActiveRepoSubscriptions(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subscriptions (subscriber_id, repo_slug, weight)
		 SELECT ?, slug, 3 FROM repos WHERE active = 1`, id,
	)
	if err != nil {
		return fmt.Errorf("add all active repo subscriptions: %w", err)
	}
	return nil
}

func (s *subQueries) Reactivate(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE subscribers SET unsubscribed_at = NULL, confirmed_at = NULL
		  WHERE email = ? AND unsubscribed_at IS NOT NULL`, email,
	)
	return err
}

func (s *subQueries) Unsubscribe(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE subscribers SET unsubscribed_at = ? WHERE email = ? AND unsubscribed_at IS NULL`,
		time.Now().UTC(), email,
	)
	return err
}

func (s *subQueries) IsUnsubscribed(ctx context.Context, email string) (bool, error) {
	var t sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT unsubscribed_at FROM subscribers WHERE email = ?`, email,
	).Scan(&t)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return t.Valid, nil
}

func (s *subQueries) ListActive(ctx context.Context) ([]Subscriber, error) {
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

func (s *subQueries) IsActive(ctx context.Context, id int64) (bool, error) {
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

func (s *subQueries) LoadSubscriptions(ctx context.Context, id int64) ([]Subscription, error) {
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
