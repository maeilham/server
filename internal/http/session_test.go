package http

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	dbpkg "github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/store"
	"github.com/maeilham/server/internal/subscriber"
)

var sessionLinkRe = regexp.MustCompile(`#t=([0-9a-f]{64})`)

// newSessionRouter는 newSubscribeRouter와 같지만, 해지 등을 store로 직접 조작할 수 있게 DB 커넥션도 돌려준다.
func newSessionRouter(t *testing.T) (http.Handler, *captureMailer, *sql.DB) {
	t.Helper()
	conn, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	if err := dbpkg.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	mailer := &captureMailer{}
	svc := subscriber.NewSubscriberService(store.NewSubscriberStore(conn), mailer, "secret", "https://api.example", "https://web.example")
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		SubSvc:  svc,
		BaseURL: "https://web.example",
	})
	return h, mailer, conn
}

// subscribeAndExtractToken은 /api/subscribe로 실제 가입한 뒤 발송된 메일에서 개인 링크 토큰을 뽑는다.
func subscribeAndExtractToken(t *testing.T, h http.Handler, mailer *captureMailer, email string) string {
	t.Helper()
	rec := postSubscribe(h, `{"email":"`+email+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("subscribe status = %d, body %s", rec.Code, rec.Body)
	}
	m := sessionLinkRe.FindStringSubmatch(mailer.sent[len(mailer.sent)-1].TextBody)
	if m == nil {
		t.Fatalf("no personal link in mail:\n%s", mailer.sent[len(mailer.sent)-1].TextBody)
	}
	return m[1]
}

func doWithBearer(h http.Handler, method, path, tok string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeSession(t *testing.T, rec *httptest.ResponseRecorder) sessionResponse {
	t.Helper()
	var out sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestSession_FirstOpenConfirmsAndSubscribes(t *testing.T) {
	h, mailer := newSubscribeRouter(t)
	tok := subscribeAndExtractToken(t, h, mailer, "me@example.com")

	rec := doWithBearer(h, http.MethodPost, "/api/session", tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	body := decodeSession(t, rec)
	if body.Status != "subscriber" || !body.NewlyConfirmed {
		t.Errorf("body = %+v, want status=subscriber, newly_confirmed=true", body)
	}
}

func TestSession_SecondOpenIsIdempotent(t *testing.T) {
	h, mailer := newSubscribeRouter(t)
	tok := subscribeAndExtractToken(t, h, mailer, "me@example.com")

	if rec := doWithBearer(h, http.MethodPost, "/api/session", tok); rec.Code != http.StatusOK {
		t.Fatalf("first call: status = %d, body %s", rec.Code, rec.Body)
	}

	rec := doWithBearer(h, http.MethodPost, "/api/session", tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	body := decodeSession(t, rec)
	if body.NewlyConfirmed {
		t.Error("newly_confirmed = true on second open, want false")
	}
}

func TestSession_UnknownToken(t *testing.T) {
	h, _ := newSubscribeRouter(t)
	unknown := "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	if rec := doWithBearer(h, http.MethodPost, "/api/session", unknown); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestSession_MalformedToken(t *testing.T) {
	h, _ := newSubscribeRouter(t)
	for _, tok := range []string{"too-short", "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"} {
		if rec := doWithBearer(h, http.MethodPost, "/api/session", tok); rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q: status = %d, want 401", tok, rec.Code)
		}
	}
}

func TestSession_MissingAuthorizationHeader(t *testing.T) {
	h, _ := newSubscribeRouter(t)
	if rec := doWithBearer(h, http.MethodPost, "/api/session", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestSession_UnsubscribedToken(t *testing.T) {
	h, mailer, conn := newSessionRouter(t)
	tok := subscribeAndExtractToken(t, h, mailer, "me@example.com")
	if rec := doWithBearer(h, http.MethodPost, "/api/session", tok); rec.Code != http.StatusOK {
		t.Fatalf("establish: status = %d, body %s", rec.Code, rec.Body)
	}
	// 해지는 별개의 HMAC 토큰 체계를 쓰므로(/api/unsubscribe), 여기서는 store를 직접 불러 해지시킨다.
	if err := store.NewSubscriberStore(conn).Unsubscribe(t.Context(), "me@example.com"); err != nil {
		t.Fatal(err)
	}

	if rec := doWithBearer(h, http.MethodPost, "/api/session", tok); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestMe_ValidConfirmedToken(t *testing.T) {
	h, mailer := newSubscribeRouter(t)
	tok := subscribeAndExtractToken(t, h, mailer, "me@example.com")
	if rec := doWithBearer(h, http.MethodPost, "/api/session", tok); rec.Code != http.StatusOK {
		t.Fatalf("establish: status = %d, body %s", rec.Code, rec.Body)
	}

	rec := doWithBearer(h, http.MethodGet, "/api/me", tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var out meResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "subscriber" {
		t.Errorf("status field = %q, want subscriber", out.Status)
	}
}

func TestMe_UnconfirmedTokenIsUnauthorizedAndNeverMutates(t *testing.T) {
	h, mailer := newSubscribeRouter(t)
	tok := subscribeAndExtractToken(t, h, mailer, "me@example.com")

	if rec := doWithBearer(h, http.MethodGet, "/api/me", tok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	// /api/me가 뭔가를 바꿔놓았다면 여기서 newly_confirmed가 false로 나올 것이다.
	rec := doWithBearer(h, http.MethodPost, "/api/session", tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	body := decodeSession(t, rec)
	if !body.NewlyConfirmed {
		t.Error("newly_confirmed = false; /api/me must not have confirmed the subscriber as a side effect")
	}
}

func TestMe_UnknownToken(t *testing.T) {
	h, _ := newSubscribeRouter(t)
	unknown := "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	if rec := doWithBearer(h, http.MethodGet, "/api/me", unknown); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestMe_MalformedToken(t *testing.T) {
	h, _ := newSubscribeRouter(t)
	if rec := doWithBearer(h, http.MethodGet, "/api/me", "not-hex-at-all"); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestMe_MissingHeader(t *testing.T) {
	h, _ := newSubscribeRouter(t)
	if rec := doWithBearer(h, http.MethodGet, "/api/me", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestSessionCORS_AllowsAuthorizationHeader(t *testing.T) {
	h, _ := newSubscribeRouter(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/session", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	got := rec.Header().Get("Access-Control-Allow-Headers")
	if !regexp.MustCompile(`(?i)Authorization`).MatchString(got) {
		t.Errorf("Access-Control-Allow-Headers = %q, want it to include Authorization", got)
	}
}
