package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	dbpkg "github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/delivery"
	"github.com/maeilham/server/internal/store"
)

type fakePicker struct {
	c   *store.Content
	err error
}

func (f *fakePicker) ForVisitor(context.Context) (*store.Content, error) { return f.c, f.err }

func getToday(t *testing.T, p TodayPicker) *httptest.ResponseRecorder {
	t.Helper()
	h := &todayHandler{picker: p, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	router := chi.NewRouter()
	router.Get("/api/today", h.handleGet)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/today", nil))
	return rec
}

func TestToday_OKShape(t *testing.T) {
	rec := getToday(t, &fakePicker{c: &store.Content{
		RepoSlug: "be", RepoName: "백엔드", ContentID: "0001",
		Title: "제목", Preview: "미리보기", Tags: `["go","cache"]`,
		DiscussionURL: "https://x", BodyPath: "content/0001-x.md",
	}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var got struct {
		Item map[string]any `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// 정확히 이 키들만: 본문·발송일·내부 경로 등이 새어 나가면 안 된다
	var keys []string
	for k := range got.Item {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"id", "preview", "repo", "repoName", "tags", "title"}; len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] || keys[2] != want[2] || keys[3] != want[3] || keys[4] != want[4] || keys[5] != want[5] {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	if got.Item["repo"] != "be" || got.Item["repoName"] != "백엔드" || got.Item["id"] != "0001" || got.Item["title"] != "제목" {
		t.Errorf("item = %v", got.Item)
	}
	if tags, _ := got.Item["tags"].([]any); len(tags) != 2 || tags[0] != "go" {
		t.Errorf("tags = %v", got.Item["tags"])
	}
}

func TestToday_NothingToShowIs404(t *testing.T) {
	rec := getToday(t, &fakePicker{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var e map[string]string
	if json.Unmarshal(rec.Body.Bytes(), &e) != nil || e["error"] == "" {
		t.Errorf("오류 본문 = %q", rec.Body)
	}
}

func TestToday_ErrorIs500(t *testing.T) {
	if rec := getToday(t, &fakePicker{err: errors.New("db down")}); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestToday_BrokenTagsBecomeEmptyArray(t *testing.T) {
	rec := getToday(t, &fakePicker{c: &store.Content{RepoSlug: "be", ContentID: "0001", Tags: "깨짐"}})
	var got struct {
		Item struct {
			Tags []string `json:"tags"`
		} `json:"item"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got.Item.Tags == nil || len(got.Item.Tags) != 0 {
		t.Errorf("status=%d body=%s (tags는 null이 아니라 []여야 함)", rec.Code, rec.Body)
	}
}

// 실제 저장소 + 실제 라우터 + 실제 TodayService: 경로 등록과 발송 전/후/다음 날 흐름을 끝까지 확인한다.
func TestToday_ThroughRealRouter(t *testing.T) {
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
		`INSERT INTO subscribers(email, confirmed_at) VALUES ('a@example.com', CURRENT_TIMESTAMP)`,
		`INSERT INTO contents(repo_slug, content_id, title, preview, tags, body_path) VALUES ('be','0001','첫 글','p','["go"]','content/0001-a.md')`,
		`INSERT INTO contents(repo_slug, content_id, title, preview, tags, body_path) VALUES ('be','0002','둘째 글','p','[]','content/0002-b.md')`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	kst := time.FixedZone("KST", 9*3600)
	now := time.Date(2026, 9, 20, 6, 0, 0, 0, kst) // 발송 전
	router := NewRouter(Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Today: &delivery.TodayService{
			Repos: store.NewRepoStore(conn), Contents: store.NewContentStore(conn), Log: store.NewDeliveryLogStore(conn),
			Loc: kst, Now: func() time.Time { return now },
		},
	})
	titleNow := func() (int, string) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/today", nil))
		var m struct {
			Item struct {
				Title string `json:"title"`
			} `json:"item"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m.Item.Title
	}

	if code, title := titleNow(); code != 200 || title != "첫 글" {
		t.Errorf("발송 전: code=%d title=%q, want 200 첫 글", code, title)
	}

	// 07:00 KST에 첫 글이 발송되고 배치가 로테이션을 넘긴 상태 (delivery_log는 UTC로 저장)
	for _, q := range []string{
		`INSERT INTO delivery_log(subscriber_id, repo_slug, content_id, sent_at) VALUES (1, 'be', '0001', '2026-09-19 22:00:00')`,
		`UPDATE contents SET rotation_count = 1 WHERE content_id = '0001'`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now = time.Date(2026, 9, 20, 15, 0, 0, 0, kst)
	if code, title := titleNow(); code != 200 || title != "첫 글" {
		t.Errorf("발송 후: code=%d title=%q, want 200 첫 글 (내일 글이 보이면 안 됨)", code, title)
	}

	now = time.Date(2026, 9, 21, 0, 10, 0, 0, kst) // 다음 날, 아직 발송 전
	if code, title := titleNow(); code != 200 || title != "둘째 글" {
		t.Errorf("다음 날: code=%d title=%q, want 200 둘째 글", code, title)
	}
}

func TestToday_NoContentIs404ThroughRealRouter(t *testing.T) {
	conn, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	router := NewRouter(Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Today: &delivery.TodayService{
			Repos: store.NewRepoStore(conn), Contents: store.NewContentStore(conn), Log: store.NewDeliveryLogStore(conn),
			Loc: time.UTC,
		},
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/today", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
