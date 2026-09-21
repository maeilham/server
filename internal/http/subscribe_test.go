package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	dbpkg "github.com/maeilham/server/internal/db"
	imail "github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/store"
	"github.com/maeilham/server/internal/subscriber"
)

type captureMailer struct{ sent []imail.Message }

func (m *captureMailer) Send(_ context.Context, msg imail.Message) error {
	m.sent = append(m.sent, msg)
	return nil
}

func newSubscribeRouter(t *testing.T) (http.Handler, *captureMailer) {
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
	mailer := &captureMailer{}
	svc := subscriber.NewSubscriberService(store.NewSubscriberStore(conn), mailer, "secret", "https://api.example", "https://web.example")
	return NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		SubSvc:  svc,
		BaseURL: "https://web.example",
	}), mailer
}

func postSubscribe(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/subscribe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSubscribe_SendsPersonalLinkMail(t *testing.T) {
	h, mailer := newSubscribeRouter(t)

	rec := postSubscribe(h, `{"email":"Me@Example.com"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if len(mailer.sent) != 1 {
		t.Fatalf("sent %d mails, want 1", len(mailer.sent))
	}
	msg := mailer.sent[0]
	if msg.To != "me@example.com" {
		t.Errorf("to = %q", msg.To)
	}
	if !regexp.MustCompile(`https://web\.example/#t=[0-9a-f]{64}`).MatchString(msg.TextBody) {
		t.Errorf("mail has no personal link:\n%s", msg.TextBody)
	}
}

// 이미 가입한 주소인지가 응답으로 드러나면 안 된다(주소 존재 여부 노출). 새 주소와 기존 주소의 응답이 같아야 한다.
func TestSubscribe_SameResponseForNewAndExistingAddress(t *testing.T) {
	h, mailer := newSubscribeRouter(t)

	first := postSubscribe(h, `{"email":"me@example.com"}`)
	second := postSubscribe(h, `{"email":"me@example.com"}`)
	if first.Code != second.Code || first.Body.String() != second.Body.String() {
		t.Errorf("responses differ: %d %q vs %d %q", first.Code, first.Body, second.Code, second.Body)
	}
	if len(mailer.sent) != 2 {
		t.Fatalf("sent %d mails, want 2 (the link is resent)", len(mailer.sent))
	}
	link := regexp.MustCompile(`#t=[0-9a-f]{64}`)
	if a, b := link.FindString(mailer.sent[0].TextBody), link.FindString(mailer.sent[1].TextBody); a == "" || a != b {
		t.Errorf("resent link differs: %q vs %q", a, b)
	}
}

func TestSubscribe_InvalidEmailSendsNothing(t *testing.T) {
	h, mailer := newSubscribeRouter(t)

	for _, body := range []string{`{"email":"nope"}`, `{"email":""}`, `not json`} {
		if rec := postSubscribe(h, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400", body, rec.Code)
		}
	}
	if len(mailer.sent) != 0 {
		t.Errorf("sent %d mails for invalid input", len(mailer.sent))
	}
}
