package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/maeilham/server/internal/content"
	dbpkg "github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/store"
)

// ── 대역 ─────────────────────────────────────────────────────────────────────

type fakeContents struct {
	c        *store.Content
	list     []*store.Content
	err      error
	called   bool
	gotRepo  string
	gotID    string
	gotLimit int
}

func (f *fakeContents) ListSummaries(_ context.Context, limit int) ([]*store.Content, error) {
	f.called, f.gotLimit = true, limit
	return f.list, f.err
}

func (f *fakeContents) GetByRepoAndID(_ context.Context, repo, id string) (*store.Content, error) {
	f.called, f.gotRepo, f.gotID = true, repo, id
	return f.c, f.err
}

type fakeBodies struct {
	body string
	err  error
	got  content.BodyRef
}

func (f *fakeBodies) Get(_ context.Context, ref content.BodyRef) (string, error) {
	f.got = ref
	return f.body, f.err
}

func sampleContent() *store.Content {
	return &store.Content{
		RepoSlug: "backend-ops", RepoName: "백엔드 · 인프라", ContentID: "0001",
		Title: "제목", Preview: "미리보기", Tags: `["go","concurrency"]`,
		BodyPath: "content/0001-x.md", GithubSHA: "abc123",
		GitHubURL:     "https://github.com/maeilham/backend-ops",
		DiscussionURL: "https://github.com/maeilham/backend-ops/discussions/1",
	}
}

// getContent는 contentHandler만 chi 라우터에 올려 요청한다.
func getContent(t *testing.T, c contentReader, b content.BodySource, path string) *httptest.ResponseRecorder {
	t.Helper()
	h := &contentHandler{contents: c, bodies: b, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	router := chi.NewRouter()
	router.Get("/api/contents", h.handleList)
	router.Get("/api/contents/{repo}/{id}", h.handleGet)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("오류 응답이 JSON이 아님: %q", rec.Body.String())
	}
	return e["error"]
}

// ── 테스트 ───────────────────────────────────────────────────────────────────

func TestContentGet_OK(t *testing.T) {
	contents := &fakeContents{c: sampleContent()}
	bodies := &fakeBodies{body: "## 본문\n\n내용"}

	rec := getContent(t, contents, bodies, "/api/contents/backend-ops/0001")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"repo": "backend-ops", "repoName": "백엔드 · 인프라", "id": "0001",
		"title": "제목", "preview": "미리보기", "body": "## 본문\n\n내용",
		"discussionUrl": "https://github.com/maeilham/backend-ops/discussions/1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	tags, _ := got["tags"].([]any)
	if len(tags) != 2 || tags[0] != "go" || tags[1] != "concurrency" {
		t.Errorf("tags = %v", got["tags"])
	}

	// 핸들러가 (repo, id)로 조회하고, 본문 조회 키(경로+SHA)를 제대로 넘겼는지
	if contents.gotRepo != "backend-ops" || contents.gotID != "0001" {
		t.Errorf("조회 키 = %s/%s", contents.gotRepo, contents.gotID)
	}
	wantRef := content.BodyRef{Owner: "maeilham", Repo: "backend-ops", Path: "content/0001-x.md", SHA: "abc123"}
	if bodies.got != wantRef {
		t.Errorf("BodyRef = %+v, want %+v", bodies.got, wantRef)
	}
}

func TestContentGet_NotFoundCases(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		contents *fakeContents
		bodies   *fakeBodies
		wantCall bool // 저장소 조회까지 갔는지
	}{
		{"없는 콘텐츠", "/api/contents/be/0001", &fakeContents{}, &fakeBodies{}, true},
		{"id 형식 오류", "/api/contents/be/abc", &fakeContents{c: sampleContent()}, &fakeBodies{}, false},
		{"id 자릿수 오류", "/api/contents/be/1", &fakeContents{c: sampleContent()}, &fakeBodies{}, false},
		{"GitHub에서 삭제된 글", "/api/contents/be/0001", &fakeContents{c: sampleContent()},
			&fakeBodies{err: fmt.Errorf("%w: x", content.ErrNotFound)}, true},
	}
	for _, tc := range cases {
		rec := getContent(t, tc.contents, tc.bodies, tc.path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", tc.name, rec.Code)
			continue
		}
		if msg := errorBody(t, rec); msg == "" {
			t.Errorf("%s: 오류 메시지가 비어 있음", tc.name)
		}
		if tc.contents.called != tc.wantCall {
			t.Errorf("%s: 저장소 조회 called=%v, want %v", tc.name, tc.contents.called, tc.wantCall)
		}
	}
}

func TestContentGet_BodyFetchFailureIs502(t *testing.T) {
	rec := getContent(t, &fakeContents{c: sampleContent()},
		&fakeBodies{err: errors.New("raw status 503")}, "/api/contents/be/0001")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (GitHub 문제와 없는 글은 구분해야 함)", rec.Code)
	}
}

func TestContentGet_StoreErrorIs500(t *testing.T) {
	rec := getContent(t, &fakeContents{err: errors.New("db down")}, &fakeBodies{}, "/api/contents/be/0001")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestContentGet_BadGitHubURLIs500(t *testing.T) {
	c := sampleContent()
	c.GitHubURL = "https://gitlab.com/x/y"
	rec := getContent(t, &fakeContents{c: c}, &fakeBodies{}, "/api/contents/be/0001")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestContentGet_BrokenTagsBecomeEmptyArray(t *testing.T) {
	for _, raw := range []string{"", "not json", "null"} {
		c := sampleContent()
		c.Tags = raw
		rec := getContent(t, &fakeContents{c: c}, &fakeBodies{body: "b"}, "/api/contents/be/0001")
		if rec.Code != http.StatusOK {
			t.Fatalf("tags=%q: status = %d", raw, rec.Code)
		}
		var got struct {
			Tags []string `json:"tags"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if got.Tags == nil || len(got.Tags) != 0 {
			t.Errorf("tags=%q: %s (null이 아니라 []여야 함)", raw, rec.Body)
		}
	}
}

func TestContentList_OKShape(t *testing.T) {
	// 한국 시간 16:00 = UTC 07:00. 응답은 항상 UTC(Z)로 나가야 한다.
	sent := time.Date(2026, 9, 10, 16, 0, 0, 0, time.FixedZone("KST", 9*3600))
	contents := &fakeContents{list: []*store.Content{
		{RepoSlug: "be", RepoName: "백엔드", ContentID: "0002", Title: "발송된 글", Preview: "p", Tags: `["go"]`, SentAt: sent},
		{RepoSlug: "be", RepoName: "백엔드", ContentID: "0001", Title: "미발송 글", Preview: "p", Tags: "깨진 JSON"},
	}}
	rec := getContent(t, contents, &fakeBodies{}, "/api/contents")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d개", len(got.Items))
	}
	a, b := got.Items[0], got.Items[1]
	if a["title"] != "발송된 글" || a["sentAt"] != "2026-09-10T07:00:00Z" {
		t.Errorf("첫 항목 = %v", a)
	}
	if tags, _ := a["tags"].([]any); len(tags) != 1 || tags[0] != "go" {
		t.Errorf("tags = %v", a["tags"])
	}
	if _, has := b["sentAt"]; has {
		t.Errorf("미발송 글에는 sentAt이 없어야 함: %v", b)
	}
	if tags, ok := b["tags"].([]any); !ok || len(tags) != 0 {
		t.Errorf("깨진 tags는 []여야 함: %v", b["tags"])
	}
	for _, it := range got.Items {
		if _, has := it["body"]; has {
			t.Errorf("목록에 body가 있으면 안 됨: %v", it)
		}
	}
}

func TestContentList_EmptyIsEmptyArrayNotNull(t *testing.T) {
	rec := getContent(t, &fakeContents{}, &fakeBodies{}, "/api/contents")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := rec.Body.String(); body != "{\"items\":[]}\n" {
		t.Errorf("body = %q, want {\"items\":[]} (null이면 웹이 깨짐)", body)
	}
}

func TestContentList_LimitParsing(t *testing.T) {
	cases := []struct {
		query string
		want  int
	}{
		{"", 50}, {"?limit=5", 5}, {"?limit=100", 100}, {"?limit=1000", 100},
		{"?limit=abc", 50}, {"?limit=0", 50}, {"?limit=-3", 50},
	}
	for _, tc := range cases {
		f := &fakeContents{}
		if rec := getContent(t, f, &fakeBodies{}, "/api/contents"+tc.query); rec.Code != http.StatusOK {
			t.Errorf("%q: status = %d", tc.query, rec.Code)
			continue
		}
		if f.gotLimit != tc.want {
			t.Errorf("%q: limit = %d, want %d", tc.query, f.gotLimit, tc.want)
		}
	}
}

func TestContentList_StoreErrorIs500(t *testing.T) {
	rec := getContent(t, &fakeContents{err: errors.New("db down")}, &fakeBodies{}, "/api/contents")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// ── 통합: 실제 저장소 + 실제 라우터 + 가짜 GitHub ─────────────────────────────

type rawFake struct {
	files map[string]string // path -> 원문
	calls int
}

func (f *rawFake) FetchRaw(_ context.Context, _, _, _, path string) ([]byte, error) {
	f.calls++
	if s, ok := f.files[path]; ok {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("%w: %s", content.ErrNotFound, path)
}

// setupRealRouter는 실제 SQLite 저장소 + 실제 라우터 + 가짜 GitHub로 서버를 구성한다.
// NewRouter에 경로가 실제로 등록되어 있는지, 저장소·본문 조회가 끝까지 이어지는지 확인하는 용도다.
func setupRealRouter(t *testing.T) (http.Handler, *rawFake) {
	t.Helper()
	conn, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, q := range []string{
		`INSERT INTO repos(slug, github_url, display_name) VALUES ('be', 'https://github.com/maeilham/be', '백엔드')`,
		`INSERT INTO repos(slug, github_url, display_name) VALUES ('fe', 'https://github.com/maeilham/fe', '프론트')`,
		`INSERT INTO contents(repo_slug, content_id, title, preview, tags, body_path, github_sha)
		 VALUES ('be', '0001', '백엔드 글', 'p', '["go"]', 'content/0001-be.md', 'sha-be')`,
		`INSERT INTO contents(repo_slug, content_id, title, preview, tags, body_path, github_sha, sent_at)
		 VALUES ('be', '0002', '발송된 글', 'p', '[]', 'content/0002-be.md', 'sha-be2', '2026-09-10 07:00:00')`,
		`INSERT INTO contents(repo_slug, content_id, title, preview, tags, body_path, github_sha)
		 VALUES ('fe', '0001', '프론트 글', 'p', '[]', 'content/0001-fe.md', 'sha-fe')`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	fake := &rawFake{files: map[string]string{
		"content/0001-be.md": "---\ntitle: \"t\"\npreview: \"p\"\n---\n\n백엔드 본문\n",
		"content/0001-fe.md": "---\ntitle: \"t\"\npreview: \"p\"\n---\n\n프론트 본문\n",
	}}
	router := NewRouter(Deps{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Contents: store.NewContentStore(conn),
		Bodies:   content.NewCachedBodySource(fake, content.BodyOptions{}),
	})
	return router, fake
}

func doGet(router http.Handler, path string) (int, map[string]any) {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func TestContentGet_ThroughRealRouter(t *testing.T) {
	router, fake := setupRealRouter(t)

	// 같은 id "0001"이라도 repo별로 다른 글과 본문이 나와야 한다.
	code, be := doGet(router, "/api/contents/be/0001")
	if code != 200 || be["title"] != "백엔드 글" || be["body"] != "백엔드 본문" {
		t.Errorf("be/0001: code=%d %v", code, be)
	}
	code, fe := doGet(router, "/api/contents/fe/0001")
	if code != 200 || fe["title"] != "프론트 글" || fe["body"] != "프론트 본문" {
		t.Errorf("fe/0001: code=%d %v", code, fe)
	}

	// 같은 글을 다시 요청하면 GitHub를 다시 부르지 않는다 (캐시)
	before := fake.calls
	doGet(router, "/api/contents/be/0001")
	if fake.calls != before {
		t.Errorf("두 번째 요청에서 GitHub를 다시 호출함 (%d -> %d)", before, fake.calls)
	}

	if code, _ := doGet(router, "/api/contents/be/9999"); code != 404 {
		t.Errorf("없는 글: code = %d, want 404", code)
	}
}

func TestContentList_ThroughRealRouter(t *testing.T) {
	router, fake := setupRealRouter(t)

	code, body := doGet(router, "/api/contents")
	if code != 200 {
		t.Fatalf("code = %d, body=%v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items = %d개, want 3: %v", len(items), body)
	}
	// 발송된 글(be/0002)이 먼저, 그 뒤에 미발송 글이 content_id 내림차순·repo 순으로 온다.
	var order []string
	for _, it := range items {
		m := it.(map[string]any)
		order = append(order, m["repo"].(string)+"/"+m["id"].(string))
	}
	if got := fmt.Sprint(order); got != "[be/0002 be/0001 fe/0001]" {
		t.Errorf("순서 = %s", got)
	}
	first := items[0].(map[string]any)
	if first["sentAt"] != "2026-09-10T07:00:00Z" || first["repoName"] != "백엔드" {
		t.Errorf("첫 항목 = %v", first)
	}
	if _, has := items[1].(map[string]any)["sentAt"]; has {
		t.Errorf("미발송 글에는 sentAt이 없어야 함: %v", items[1])
	}
	for _, it := range items {
		if _, has := it.(map[string]any)["body"]; has {
			t.Errorf("목록에 본문이 들어가면 안 됨: %v", it)
		}
	}
	if fake.calls != 0 {
		t.Errorf("목록은 GitHub를 호출하지 않아야 함 (호출 %d회)", fake.calls)
	}
}
