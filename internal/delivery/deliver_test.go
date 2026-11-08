package delivery

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
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

func notifyOpts(db *sql.DB, day time.Time) NotifyOptions {
	return NotifyOptions{
		Loc:           testLoc,
		Day:           day,
		BaseURL:       "https://web.example",
		Secret:        "test-secret",
		SubRepo:       store.NewSubscriberStore(db),
		RepoStore:     store.NewRepoStore(db),
		ContentStore:  store.NewContentStore(db),
		QuestionStore: store.NewDailyQuestionStore(db),
		LogStore:      store.NewDeliveryLogStore(db),
	}
}

func morning(d int) time.Time { return time.Date(2026, 6, d, 7, 30, 0, 0, testLoc) }

func seedOneSubscriber(t *testing.T, db *sql.DB, contents ...string) int64 {
	t.Helper()
	id := insertConfirmedSubscriber(t, db, "me@example.com")
	insertRepo(t, db, "be")
	for _, c := range contents {
		insertContent(t, db, "be", c)
	}
	subscribe(t, db, id, "be", 3)
	return id
}

func TestNotifyNewQuestions_SendsTitleOfTodaysNewQuestionOncePerDay(t *testing.T) {
	db := newTestDB(t)
	seedOneSubscriber(t, db, "0001", "0002")
	m := &mockMailer{}

	stats, err := NotifyNewQuestions(context.Background(), discardLogger(), m, notifyOpts(db, morning(1)))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if stats.NewQuestions != 1 || stats.Sent != 1 || stats.Errors != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	msg := m.messages[0]
	if msg.To != "me@example.com" || !strings.Contains(msg.Subject, "title-0001") || !strings.Contains(msg.TextBody, "지금 확인하기 → https://web.example/q/be/0001\n") {
		t.Errorf("unexpected mail: %+v", msg)
	}

	// 같은 날 다시 돌려도(재시작, 스케줄러 재실행) 다시 보내지 않는다
	stats, _ = NotifyNewQuestions(context.Background(), discardLogger(), m, notifyOpts(db, morning(1).Add(3*time.Hour)))
	if stats.NewQuestions != 0 || len(m.messages) != 1 {
		t.Errorf("second run: stats=%+v mails=%d, want no new mail", stats, len(m.messages))
	}
}

func TestNotifyNewQuestions_NoMailOnReviewDay(t *testing.T) {
	db := newTestDB(t)
	seedOneSubscriber(t, db, "0001", "0002")
	m := &mockMailer{}
	ctx := context.Background()

	_, _ = NotifyNewQuestions(ctx, discardLogger(), m, notifyOpts(db, morning(1)))
	_, _ = NotifyNewQuestions(ctx, discardLogger(), m, notifyOpts(db, morning(2)))
	if len(m.messages) != 2 {
		t.Fatalf("new questions on day 1 and 2 must be mailed, got %d mails", len(m.messages))
	}

	stats, err := NotifyNewQuestions(ctx, discardLogger(), m, notifyOpts(db, morning(3))) // 안 나온 글이 없어 복습
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if stats.NewQuestions != 0 || len(m.messages) != 2 {
		t.Errorf("review day: stats=%+v mails=%d, want no mail", stats, len(m.messages))
	}
}

func TestNotifyNewQuestions_DecidesQuestionWithoutAnyVisit(t *testing.T) {
	db := newTestDB(t)
	seedOneSubscriber(t, db, "0001")

	_, _ = NotifyNewQuestions(context.Background(), discardLogger(), &mockMailer{}, notifyOpts(db, morning(1)))

	q, err := store.NewDailyQuestionStore(db).Get(context.Background(), "2026-06-01", "be")
	if err != nil || q == nil || q.ContentID != "0001" || !q.Notified {
		t.Errorf("daily question = %+v err=%v, want 0001 decided and notified", q, err)
	}
}

func TestNotifyNewQuestions_OnlySubscribersOfThatRepo(t *testing.T) {
	db := newTestDB(t)
	idBe := insertConfirmedSubscriber(t, db, "be@x.kr")
	idOther := insertConfirmedSubscriber(t, db, "other@x.kr")
	idPaused := insertConfirmedSubscriber(t, db, "paused@x.kr")
	mustExec(t, db, `UPDATE subscribers SET paused_at = CURRENT_TIMESTAMP WHERE id = ?`, idPaused)
	insertRepo(t, db, "be")
	insertRepo(t, db, "other")
	insertContent(t, db, "be", "0001")
	subscribe(t, db, idBe, "be", 3)
	subscribe(t, db, idOther, "other", 3)
	subscribe(t, db, idPaused, "be", 3)
	m := &mockMailer{}

	stats, _ := NotifyNewQuestions(context.Background(), discardLogger(), m, notifyOpts(db, morning(1)))

	if len(m.messages) != 1 || m.messages[0].To != "be@x.kr" {
		t.Fatalf("mails = %+v, want only be@x.kr", m.messages)
	}
	if stats.Subscribers != 2 || stats.Skipped != 1 {
		t.Errorf("stats = %+v, want 2 active subscribers and 1 skipped (other repo)", stats)
	}
}

// 한 사람이 여러 repo를 구독해도 새 질문들을 한 통에 담는다.
func TestNotifyNewQuestions_OneMailForSeveralRepos(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "me@example.com")
	insertRepo(t, db, "be")
	insertRepo(t, db, "ops")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "ops", "0001")
	subscribe(t, db, id, "be", 3)
	subscribe(t, db, id, "ops", 3)
	m := &mockMailer{}

	_, _ = NotifyNewQuestions(context.Background(), discardLogger(), m, notifyOpts(db, morning(1)))

	if len(m.messages) != 1 || !strings.Contains(m.messages[0].Subject, "2개") {
		t.Fatalf("mails = %+v, want one mail for 2 questions", m.messages)
	}
	for _, link := range []string{"https://web.example/q/be/0001", "https://web.example/q/ops/0001"} {
		if !strings.Contains(m.messages[0].TextBody, link) {
			t.Errorf("mail has no link to its own question %s:\n%s", link, m.messages[0].TextBody)
		}
	}
}

// 해지 화면은 웹에 있고 API 서버에는 `/`가 없어서, 해지 링크는 웹 주소여야 한다.
func TestNotifyNewQuestions_UnsubscribeLinkPointsToWeb(t *testing.T) {
	db := newTestDB(t)
	seedOneSubscriber(t, db, "0001")
	m := &mockMailer{}

	_, _ = NotifyNewQuestions(context.Background(), discardLogger(), m, notifyOpts(db, morning(1)))

	if want := "구독 해지 → https://web.example/?action=unsubscribe&token="; !strings.Contains(m.messages[0].TextBody, want) {
		t.Errorf("text body has no web unsubscribe link (want %q)\n%s", want, m.messages[0].TextBody)
	}
}

func TestNotifyNewQuestions_DryRunHasNoSideEffects(t *testing.T) {
	db := newTestDB(t)
	seedOneSubscriber(t, db, "0001")
	m := &mockMailer{}
	opts := notifyOpts(db, morning(1))
	opts.DryRun = true

	stats, err := NotifyNewQuestions(context.Background(), discardLogger(), m, opts)
	if err != nil || stats.Sent != 1 {
		t.Fatalf("stats=%+v err=%v, want dry-run to report one pending mail", stats, err)
	}
	var logs int
	_ = db.QueryRow(`SELECT COUNT(*) FROM delivery_log`).Scan(&logs)
	q, _ := store.NewDailyQuestionStore(db).Get(context.Background(), "2026-06-01", "be")
	if len(m.messages) != 0 || logs != 0 || q == nil || q.Notified {
		t.Errorf("dry-run side effects: mails=%d logs=%d question=%+v", len(m.messages), logs, q)
	}

	// dry-run 뒤 실제 실행은 정상적으로 보낸다
	_, _ = NotifyNewQuestions(context.Background(), discardLogger(), m, notifyOpts(db, morning(1)))
	if len(m.messages) != 1 {
		t.Errorf("real run after dry-run: mails=%d, want 1", len(m.messages))
	}
}

// 일부 발송이 실패하면 질문을 완료로 표시하지 않아서, 다음 실행이 실패한 사람에게만 보낸다.
func TestNotifyNewQuestions_RetriesOnlyFailedSubscribers(t *testing.T) {
	db := newTestDB(t)
	idOK := insertConfirmedSubscriber(t, db, "ok@x.kr")
	idFail := insertConfirmedSubscriber(t, db, "fail@x.kr")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	subscribe(t, db, idOK, "be", 3)
	subscribe(t, db, idFail, "be", 3)
	m := &mockMailer{failOn: "fail@x.kr"}
	ctx := context.Background()

	stats, _ := NotifyNewQuestions(ctx, discardLogger(), m, notifyOpts(db, morning(1)))
	if stats.Sent != 1 || stats.Errors != 1 {
		t.Fatalf("first run stats = %+v", stats)
	}

	m.failOn = ""
	stats, _ = NotifyNewQuestions(ctx, discardLogger(), m, notifyOpts(db, morning(1).Add(5*time.Minute)))
	if stats.Sent != 1 || stats.Errors != 0 || len(m.messages) != 2 {
		t.Fatalf("retry stats = %+v mails=%d, want only the failed one resent", stats, len(m.messages))
	}
	tos := []string{m.messages[0].To, m.messages[1].To}
	if tos[0] == tos[1] {
		t.Errorf("same person mailed twice: %v", tos)
	}

	stats, _ = NotifyNewQuestions(ctx, discardLogger(), m, notifyOpts(db, morning(1).Add(10*time.Minute)))
	if stats.NewQuestions != 0 {
		t.Errorf("after full success the question must be marked done, got %+v", stats)
	}
}

func TestNotifyDue(t *testing.T) {
	at := 7*time.Hour + 30*time.Minute
	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"just before", time.Date(2026, 6, 1, 7, 29, 59, 0, testLoc), false},
		{"exactly", time.Date(2026, 6, 1, 7, 30, 0, 0, testLoc), true},
		{"afternoon", time.Date(2026, 6, 1, 15, 0, 0, 0, testLoc), true},
		{"after midnight is the new day", time.Date(2026, 6, 2, 0, 5, 0, 0, testLoc), false},
		// 같은 순간을 UTC로 표기해도 서비스 시간대 기준으로 판단한다(UTC 22:00 = KST 07:00 다음 날)
		{"utc spelling of 07:00 KST", time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC), false},
		{"utc spelling of 07:30 KST", time.Date(2026, 5, 31, 22, 30, 0, 0, time.UTC), true},
	}
	for _, tc := range cases {
		if got := NotifyDue(tc.now, testLoc, at); got != tc.want {
			t.Errorf("%s: NotifyDue(%s) = %v, want %v", tc.name, tc.now, got, tc.want)
		}
	}
}

func TestQuestionURL(t *testing.T) {
	cases := []struct{ base, repo, id, want string }{
		{"https://web.example", "bops", "0005", "https://web.example/q/bops/0005"},
		{"https://web.example/", "bops", "0005", "https://web.example/q/bops/0005"}, // 끝 슬래시는 한 번만
		{"https://web.example", "a b/c", "0001", "https://web.example/q/a%20b%2Fc/0001"},
	}
	for _, tc := range cases {
		if got := questionURL(tc.base, tc.repo, tc.id); got != tc.want {
			t.Errorf("questionURL(%q, %q, %q) = %q, want %q", tc.base, tc.repo, tc.id, got, tc.want)
		}
	}
}
