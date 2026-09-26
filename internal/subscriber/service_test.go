package subscriber

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	dbpkg "github.com/maeilham/server/internal/db"
	imail "github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/store"
)

type fakeMailer struct {
	sent []imail.Message
	err  error
}

func (m *fakeMailer) Send(_ context.Context, msg imail.Message) error {
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, msg)
	return nil
}

func newTestService(t *testing.T) (*SubscriberService, *fakeMailer, store.SubscriberRepository, func(email string) (token string, confirmed bool)) {
	t.Helper()
	conn, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1) // :memory:는 연결마다 별도 DB라 하나로 고정
	t.Cleanup(func() { _ = conn.Close() })
	if err := dbpkg.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	mailer := &fakeMailer{}
	repo := store.NewSubscriberStore(conn)
	svc := NewSubscriberService(repo, mailer, "test-secret", "https://api.example", "https://web.example/")

	row := func(email string) (string, bool) {
		t.Helper()
		var tok *string
		var confirmed bool
		if err := conn.QueryRow(`SELECT access_token, confirmed_at IS NOT NULL FROM subscribers WHERE email = ?`, email).Scan(&tok, &confirmed); err != nil {
			t.Fatalf("read subscriber %s: %v", email, err)
		}
		if tok == nil {
			return "", confirmed
		}
		return *tok, confirmed
	}
	return svc, mailer, repo, row
}

var linkRe = regexp.MustCompile(`https://web\.example/#t=([0-9a-f]{64})`)

func TestSubscribe_SendsPersonalLink(t *testing.T) {
	svc, mailer, _, row := newTestService(t)

	if err := svc.Subscribe(context.Background(), "  Me@Example.com ", nil); err != nil {
		t.Fatal(err)
	}

	if len(mailer.sent) != 1 {
		t.Fatalf("sent %d mails, want 1", len(mailer.sent))
	}
	msg := mailer.sent[0]
	if msg.To != "me@example.com" {
		t.Errorf("to = %q, want the normalized address", msg.To)
	}
	m := linkRe.FindStringSubmatch(msg.TextBody)
	if m == nil {
		t.Fatalf("text body has no personal link:\n%s", msg.TextBody)
	}
	tok, confirmed := row("me@example.com")
	if m[1] != tok {
		t.Errorf("link token %q != stored token %q", m[1], tok)
	}
	// 링크를 열기 전에는 가입이 완료된 상태가 아니다(링크를 처음 여는 것이 이메일 인증)
	if confirmed {
		t.Error("subscriber is confirmed before opening the link")
	}
	if strings.Contains(msg.TextBody, "/api/confirm") {
		t.Error("the old confirmation link leaked into the personal-link mail")
	}
	// 링크가 웹 주소여야 한다. API 주소로 가면 해지 링크 때처럼 404가 난다
	if strings.Contains(msg.TextBody, "api.example") || strings.Contains(msg.HTMLBody, "api.example") {
		t.Error("the mail links to the API server")
	}
}

func TestSubscribe_ResendsSameLink(t *testing.T) {
	svc, mailer, _, row := newTestService(t)
	ctx := context.Background()

	for range 2 {
		if err := svc.Subscribe(ctx, "me@example.com", nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(mailer.sent) != 2 {
		t.Fatalf("sent %d mails, want 2", len(mailer.sent))
	}
	a := linkRe.FindStringSubmatch(mailer.sent[0].TextBody)
	b := linkRe.FindStringSubmatch(mailer.sent[1].TextBody)
	if a == nil || b == nil || a[1] != b[1] {
		t.Errorf("resent link differs from the first one: %v vs %v", a, b)
	}
	if tok, _ := row("me@example.com"); tok != a[1] {
		t.Errorf("stored token %q != link token %q", tok, a[1])
	}
}

// 해지했다가 다시 가입해도 링크(토큰)는 그대로이고, 다시 링크를 열어야 가입이 완료된다.
func TestSubscribe_AfterUnsubscribeKeepsToken(t *testing.T) {
	svc, mailer, repo, row := newTestService(t)
	ctx := context.Background()

	if err := svc.Subscribe(ctx, "me@example.com", nil); err != nil {
		t.Fatal(err)
	}
	first, _ := row("me@example.com")
	if _, err := repo.SetConfirmed(ctx, "me@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Unsubscribe(ctx, "me@example.com"); err != nil {
		t.Fatal(err)
	}

	if err := svc.Subscribe(ctx, "me@example.com", nil); err != nil {
		t.Fatal(err)
	}
	tok, confirmed := row("me@example.com")
	if tok != first {
		t.Errorf("token changed after resubscribe: %q -> %q", first, tok)
	}
	if confirmed {
		t.Error("resubscribed subscriber should have to open the link again")
	}
	if got := linkRe.FindStringSubmatch(mailer.sent[len(mailer.sent)-1].TextBody); got == nil || got[1] != first {
		t.Errorf("mail does not carry the original link: %v", got)
	}
}

// 터미널의 repo 선택 흐름은 가중치를 확인 링크에 실어야 해서 옛 확인 메일을 그대로 쓴다.
func TestSubscribe_WithRepoWeightsUsesConfirmMail(t *testing.T) {
	svc, mailer, _, _ := newTestService(t)

	if err := svc.Subscribe(context.Background(), "me@example.com", map[string]int{"bops": 5}); err != nil {
		t.Fatal(err)
	}
	body := mailer.sent[0].TextBody
	if !strings.Contains(body, "https://api.example/api/confirm?token=") || !strings.Contains(body, "repos=bops:5") {
		t.Errorf("want the legacy confirm link with repo weights, got:\n%s", body)
	}
	if strings.Contains(body, "#t=") {
		t.Error("personal link used for the weighted flow")
	}
}

func TestSubscribe_MailerFailureIsReturned(t *testing.T) {
	svc, mailer, _, _ := newTestService(t)
	mailer.err = errors.New("smtp down")

	if err := svc.Subscribe(context.Background(), "me@example.com", nil); err == nil {
		t.Fatal("want the mailer error to be returned")
	}
}

func TestPersonalLinkURL(t *testing.T) {
	for _, base := range []string{"https://web.example", "https://web.example/"} {
		svc := &SubscriberService{baseURL: base}
		if got, want := svc.PersonalLinkURL("abc"), "https://web.example/#t=abc"; got != want {
			t.Errorf("base %q: got %q, want %q", base, got, want)
		}
	}
}
