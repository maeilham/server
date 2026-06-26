package delivery

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/store"
)

type mockMailer struct {
	mu       sync.Mutex
	messages []mail.Message
	failOn   string // if non-empty, fails when To matches
}

func (m *mockMailer) Send(_ context.Context, msg mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failOn != "" && msg.To == m.failOn {
		return errors.New("mock send failure")
	}
	m.messages = append(m.messages, msg)
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}


func TestDailySend_HappyPath(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "me@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	subscribe(t, db, id, "be", 3)

	ss := store.NewSubscriberStore(db)
	cs := store.NewContentStore(db)
	ls := store.NewDeliveryLogStore(db)
	rs := store.NewRepoStore(db)
	mockM := &mockMailer{}
	day := time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC)

	stats, err := DailySend(context.Background(), discardLogger(), mockM, DailySendOptions{
		Day:          day,
		BaseURL:      "https://maeilham.kr",
		SubRepo:      ss,
		RepoStore:    rs,
		ContentStore: cs,
		LogStore:     ls,
	})
	if err != nil {
		t.Fatalf("DailySend: %v", err)
	}
	if stats.Subscribers != 1 || stats.Picked != 1 || stats.Sent != 1 || stats.Errors != 0 {
		t.Errorf("unexpected stats: %+v", stats)
	}
	if stats.Advanced != 1 {
		t.Errorf("expected 1 content advanced, got %d", stats.Advanced)
	}
	if len(mockM.messages) != 1 {
		t.Fatalf("expected 1 mail sent, got %d", len(mockM.messages))
	}
	msg := mockM.messages[0]
	if msg.To != "me@example.com" {
		t.Errorf("to mismatch: %q", msg.To)
	}
	if msg.Subject == "" || msg.TextBody == "" {
		t.Errorf("subject/body empty: subj=%q text=%q", msg.Subject, msg.TextBody)
	}
}

func TestDailySend_IdempotentSameDay(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "me@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	subscribe(t, db, id, "be", 3)

	ss := store.NewSubscriberStore(db)
	cs := store.NewContentStore(db)
	ls := store.NewDeliveryLogStore(db)
	rs := store.NewRepoStore(db)
	mockM := &mockMailer{}
	day := time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC)
	opts := DailySendOptions{Day: day, SubRepo: ss, RepoStore: rs, ContentStore: cs, LogStore: ls}

	_, _ = DailySend(context.Background(), discardLogger(), mockM, opts)
	stats2, _ := DailySend(context.Background(), discardLogger(), mockM, opts)

	if stats2.Sent != 0 {
		t.Errorf("retry should send 0; got %d", stats2.Sent)
	}
	if stats2.Skipped != 1 {
		t.Errorf("retry should skip 1; got %d", stats2.Skipped)
	}
	if len(mockM.messages) != 1 {
		t.Errorf("mailer should be called only once total; got %d", len(mockM.messages))
	}
}

func TestDailySend_DryRunNoSideEffects(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "me@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	subscribe(t, db, id, "be", 3)

	ss := store.NewSubscriberStore(db)
	cs := store.NewContentStore(db)
	ls := store.NewDeliveryLogStore(db)
	rs := store.NewRepoStore(db)
	mockM := &mockMailer{}
	day := time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC)

	stats, err := DailySend(context.Background(), discardLogger(), mockM, DailySendOptions{
		Day:          day,
		DryRun:       true,
		SubRepo:      ss,
		RepoStore:    rs,
		ContentStore: cs,
		LogStore:     ls,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if stats.Sent != 1 || stats.Picked != 1 {
		t.Errorf("dry-run should report picks but not actually send; got %+v", stats)
	}
	if len(mockM.messages) != 0 {
		t.Errorf("dry-run should not call mailer; got %d messages", len(mockM.messages))
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM delivery_log`).Scan(&n)
	if n != 0 {
		t.Errorf("dry-run should not write delivery_log; got %d rows", n)
	}
	var rotation int
	_ = db.QueryRow(`SELECT rotation_count FROM contents WHERE content_id = '0001'`).Scan(&rotation)
	if rotation != 0 {
		t.Errorf("dry-run should not advance rotation; got %d", rotation)
	}
}

func TestDailySend_AdvanceProgressesContent(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "me@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	subscribe(t, db, id, "be", 3)

	ss := store.NewSubscriberStore(db)
	cs := store.NewContentStore(db)
	ls := store.NewDeliveryLogStore(db)
	rs := store.NewRepoStore(db)
	mockM := &mockMailer{}
	day1 := time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)

	_, _ = DailySend(context.Background(), discardLogger(), mockM, DailySendOptions{Day: day1, SubRepo: ss, RepoStore: rs, ContentStore: cs, LogStore: ls})
	_, _ = DailySend(context.Background(), discardLogger(), mockM, DailySendOptions{Day: day2, SubRepo: ss, RepoStore: rs, ContentStore: cs, LogStore: ls})

	if len(mockM.messages) != 2 {
		t.Fatalf("expected 2 messages over 2 days; got %d", len(mockM.messages))
	}
	if mockM.messages[0].Subject == mockM.messages[1].Subject {
		t.Errorf("expected different content across days, both = %q", mockM.messages[0].Subject)
	}
}

func TestDailySend_AdvanceIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "me@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	subscribe(t, db, id, "be", 3)

	ss := store.NewSubscriberStore(db)
	cs := store.NewContentStore(db)
	ls := store.NewDeliveryLogStore(db)
	rs := store.NewRepoStore(db)
	mockM := &mockMailer{}
	day := time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC)
	opts := DailySendOptions{Day: day, SubRepo: ss, RepoStore: rs, ContentStore: cs, LogStore: ls}

	_, _ = DailySend(context.Background(), discardLogger(), mockM, opts)
	_, _ = DailySend(context.Background(), discardLogger(), mockM, opts)
	_, _ = DailySend(context.Background(), discardLogger(), mockM, opts)

	var rotation int
	_ = db.QueryRow(`SELECT rotation_count FROM contents WHERE content_id = '0001'`).Scan(&rotation)
	if rotation != 1 {
		t.Errorf("rotation_count must remain 1 after 3 same-day runs; got %d", rotation)
	}
}

func TestDailySend_SkipsPausedAndNoContent(t *testing.T) {
	db := newTestDB(t)
	idActive := insertConfirmedSubscriber(t, db, "active@x.kr")
	idPaused := insertConfirmedSubscriber(t, db, "paused@x.kr")
	mustExec(t, db, `UPDATE subscribers SET paused_at = CURRENT_TIMESTAMP WHERE id = ?`, idPaused)

	idNoContent := insertConfirmedSubscriber(t, db, "empty@x.kr")
	insertRepo(t, db, "be")
	insertRepo(t, db, "empty")
	insertContent(t, db, "be", "0001")
	subscribe(t, db, idActive, "be", 3)
	subscribe(t, db, idNoContent, "empty", 3)

	ss := store.NewSubscriberStore(db)
	cs := store.NewContentStore(db)
	ls := store.NewDeliveryLogStore(db)
	rs := store.NewRepoStore(db)
	mockM := &mockMailer{}
	stats, _ := DailySend(context.Background(), discardLogger(), mockM, DailySendOptions{
		Day:          time.Now().UTC(),
		SubRepo:      ss,
		RepoStore:    rs,
		ContentStore: cs,
		LogStore:     ls,
	})

	if stats.Subscribers != 2 {
		t.Errorf("paused should be excluded from Subscribers; got %d", stats.Subscribers)
	}
	if stats.Sent != 1 {
		t.Errorf("only active subscriber should receive; got Sent=%d", stats.Sent)
	}
	if stats.NoContent != 1 {
		t.Errorf("empty-repo subscriber should be NoContent=1; got %d", stats.NoContent)
	}
}
