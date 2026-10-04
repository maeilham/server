package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
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
	if out.Email != "me@example.com" {
		t.Errorf("email = %q, want me@example.com", out.Email)
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

func decodeSubscriptions(t *testing.T, rec *httptest.ResponseRecorder) subscriptionsResponse {
	t.Helper()
	var out subscriptionsResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestSubscriptions_ListsActiveReposWithEnabledFlag(t *testing.T) {
	h, mailer, conn := newSessionRouter(t)
	repos := store.NewRepoStore(conn)
	for _, r := range []*store.Repo{
		{Slug: "backend", GitHubURL: "https://github.com/x/backend", DisplayName: "백엔드", Description: "서버, 인프라"},
		{Slug: "front", GitHubURL: "https://github.com/x/front", DisplayName: "프론트엔드"},
	} {
		if err := repos.Upsert(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	tok := subscribeAndExtractToken(t, h, mailer, "me@example.com")
	if rec := doWithBearer(h, http.MethodPost, "/api/session", tok); rec.Code != http.StatusOK {
		t.Fatalf("establish: status = %d, body %s", rec.Code, rec.Body)
	}
	// 가입을 완료하면 활성 repo가 모두 켜진다. 하나를 꺼서 enabled가 섞인 상태를 만든다
	if _, err := conn.Exec(`DELETE FROM subscriptions WHERE repo_slug = 'front'`); err != nil {
		t.Fatal(err)
	}

	rec := doWithBearer(h, http.MethodGet, "/api/me/subscriptions", tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	got := decodeSubscriptions(t, rec).Items
	want := []subscriptionItem{
		{Repo: "backend", Name: "백엔드", Description: "서버, 인프라", Enabled: true},
		{Repo: "front", Name: "프론트엔드", Description: "", Enabled: false},
	}
	if len(got) != len(want) {
		t.Fatalf("items = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSubscriptions_NoReposIsEmptyArray(t *testing.T) {
	h, mailer, _ := newSessionRouter(t)
	tok := subscribeAndExtractToken(t, h, mailer, "me@example.com")
	if rec := doWithBearer(h, http.MethodPost, "/api/session", tok); rec.Code != http.StatusOK {
		t.Fatalf("establish: status = %d, body %s", rec.Code, rec.Body)
	}

	rec := doWithBearer(h, http.MethodGet, "/api/me/subscriptions", tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	// 웹이 null이 아니라 []를 받아야 .map/.length가 안전하다
	if body := strings.TrimSpace(rec.Body.String()); body != `{"items":[]}` {
		t.Errorf("body = %s, want {\"items\":[]}", body)
	}
}

func TestSubscriptions_Unauthorized(t *testing.T) {
	h, mailer, conn := newSessionRouter(t)
	unconfirmed := subscribeAndExtractToken(t, h, mailer, "me@example.com") // 링크를 아직 안 열었다
	unknown := "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"

	confirmed := subscribeAndExtractToken(t, h, mailer, "gone@example.com")
	if rec := doWithBearer(h, http.MethodPost, "/api/session", confirmed); rec.Code != http.StatusOK {
		t.Fatalf("establish: status = %d, body %s", rec.Code, rec.Body)
	}
	if err := store.NewSubscriberStore(conn).Unsubscribe(context.Background(), "gone@example.com"); err != nil {
		t.Fatal(err)
	}

	for name, tok := range map[string]string{
		"no token":     "",
		"malformed":    "not-hex-at-all",
		"unknown":      unknown,
		"unconfirmed":  unconfirmed,
		"unsubscribed": confirmed,
	} {
		if rec := doWithBearer(h, http.MethodGet, "/api/me/subscriptions", tok); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
	}
}

func putSubscription(h http.Handler, repo, tok, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/me/subscriptions/"+repo, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// newConfirmedSubscriber는 repo 두 개를 만들고 가입을 끝낸(둘 다 켜진) 구독자의 토큰을 돌려준다.
func newConfirmedSubscriber(t *testing.T) (http.Handler, string) {
	t.Helper()
	h, mailer, conn := newSessionRouter(t)
	repos := store.NewRepoStore(conn)
	for _, slug := range []string{"backend", "front"} {
		if err := repos.Upsert(context.Background(), &store.Repo{Slug: slug, GitHubURL: "https://github.com/x/" + slug, DisplayName: slug}); err != nil {
			t.Fatal(err)
		}
	}
	tok := subscribeAndExtractToken(t, h, mailer, "me@example.com")
	if rec := doWithBearer(h, http.MethodPost, "/api/session", tok); rec.Code != http.StatusOK {
		t.Fatalf("establish: status = %d, body %s", rec.Code, rec.Body)
	}
	return h, tok
}

func TestSetSubscription_TogglesAndShowsInList(t *testing.T) {
	h, tok := newConfirmedSubscriber(t)

	rec := putSubscription(h, "front", tok, `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("off: status = %d, body %s", rec.Code, rec.Body)
	}
	var out setSubscriptionResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil || out.Repo != "front" || out.Enabled {
		t.Errorf("response = %+v (err %v), want repo=front enabled=false", out, err)
	}
	items := decodeSubscriptions(t, doWithBearer(h, http.MethodGet, "/api/me/subscriptions", tok)).Items
	if len(items) != 2 || !items[0].Enabled || items[1].Enabled {
		t.Errorf("after off: items = %+v, want backend on / front off", items)
	}

	if rec := putSubscription(h, "front", tok, `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("on: status = %d, body %s", rec.Code, rec.Body)
	}
	items = decodeSubscriptions(t, doWithBearer(h, http.MethodGet, "/api/me/subscriptions", tok)).Items
	if len(items) != 2 || !items[0].Enabled || !items[1].Enabled {
		t.Errorf("after on: items = %+v, want both on", items)
	}
}

func TestSetSubscription_Errors(t *testing.T) {
	h, tok := newConfirmedSubscriber(t)

	cases := []struct {
		name, repo, tok, body string
		want                  int
	}{
		{"no token", "front", "", `{"enabled":false}`, http.StatusUnauthorized},
		{"unknown token", "front", "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd", `{"enabled":false}`, http.StatusUnauthorized},
		{"unknown repo", "nope", tok, `{"enabled":false}`, http.StatusNotFound},
		{"not json", "front", tok, `enabled`, http.StatusBadRequest},
		{"missing enabled", "front", tok, `{}`, http.StatusBadRequest},
		{"wrong type", "front", tok, `{"enabled":"yes"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := putSubscription(h, c.repo, c.tok, c.body); rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (body %s)", c.name, rec.Code, c.want, rec.Body)
		}
	}
	// 실패한 요청이 구독을 바꾸지 않았다
	items := decodeSubscriptions(t, doWithBearer(h, http.MethodGet, "/api/me/subscriptions", tok)).Items
	if len(items) != 2 || !items[0].Enabled || !items[1].Enabled {
		t.Errorf("items = %+v, want both still on", items)
	}
}

func TestCORS_AllowsPut(t *testing.T) {
	h, _ := newSubscribeRouter(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/me/subscriptions/front", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "PUT") {
		t.Errorf("Access-Control-Allow-Methods = %q, want it to include PUT", got)
	}
}

func TestUnsubscribeMe_UnsubscribesAndInvalidatesToken(t *testing.T) {
	h, mailer, conn := newSessionRouter(t)
	tok := subscribeAndExtractToken(t, h, mailer, "me@example.com")
	if rec := doWithBearer(h, http.MethodPost, "/api/session", tok); rec.Code != http.StatusOK {
		t.Fatalf("establish: status = %d, body %s", rec.Code, rec.Body)
	}
	otherTok := subscribeAndExtractToken(t, h, mailer, "other@example.com")
	if rec := doWithBearer(h, http.MethodPost, "/api/session", otherTok); rec.Code != http.StatusOK {
		t.Fatalf("establish other: status = %d, body %s", rec.Code, rec.Body)
	}

	if rec := doWithBearer(h, http.MethodPost, "/api/me/unsubscribe", tok); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	var unsubscribed bool
	if err := conn.QueryRow(`SELECT unsubscribed_at IS NOT NULL FROM subscribers WHERE email = 'me@example.com'`).Scan(&unsubscribed); err != nil || !unsubscribed {
		t.Errorf("unsubscribed_at set = %v (err %v), want true", unsubscribed, err)
	}
	// 같은 토큰은 이제 401이고, 요청을 다시 보내도 401이다
	if rec := doWithBearer(h, http.MethodGet, "/api/me", tok); rec.Code != http.StatusUnauthorized {
		t.Errorf("/api/me after unsubscribe: status = %d, want 401", rec.Code)
	}
	if rec := doWithBearer(h, http.MethodPost, "/api/me/unsubscribe", tok); rec.Code != http.StatusUnauthorized {
		t.Errorf("second unsubscribe: status = %d, want 401", rec.Code)
	}
	// 다른 구독자는 그대로다
	if rec := doWithBearer(h, http.MethodGet, "/api/me", otherTok); rec.Code != http.StatusOK {
		t.Errorf("other subscriber: status = %d, want 200", rec.Code)
	}
}

func TestUnsubscribeMe_Unauthorized(t *testing.T) {
	h, mailer, conn := newSessionRouter(t)
	unconfirmed := subscribeAndExtractToken(t, h, mailer, "me@example.com") // 링크를 아직 안 열었다

	for name, tok := range map[string]string{
		"no token":    "",
		"malformed":   "not-hex-at-all",
		"unknown":     "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		"unconfirmed": unconfirmed,
	} {
		if rec := doWithBearer(h, http.MethodPost, "/api/me/unsubscribe", tok); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
	}
	var unsubscribed bool
	if err := conn.QueryRow(`SELECT unsubscribed_at IS NOT NULL FROM subscribers WHERE email = 'me@example.com'`).Scan(&unsubscribed); err != nil || unsubscribed {
		t.Errorf("unsubscribed_at set = %v (err %v), want false: a rejected request must not unsubscribe", unsubscribed, err)
	}
}
