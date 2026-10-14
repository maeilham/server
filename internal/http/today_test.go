package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	dbpkg "github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/delivery"
	"github.com/maeilham/server/internal/store"
)

type fakePicker struct {
	c   *delivery.Question
	err error
}

func (f *fakePicker) ForGuest(context.Context) (*delivery.Question, error) { return f.c, f.err }

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
	rec := getToday(t, &fakePicker{c: &delivery.Question{Content: &store.Content{
		RepoSlug: "be", RepoName: "백엔드", ContentID: "0001",
		Title: "제목", Preview: "미리보기", Tags: `["go","cache"]`,
		DiscussionURL: "https://x", BodyPath: "content/0001-x.md",
	}}})
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
	if want := []string{"id", "preview", "repo", "repoName", "review", "tags", "title"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	if got.Item["review"] != false {
		t.Errorf("review = %v, want false for a new question", got.Item["review"])
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
	rec := getToday(t, &fakePicker{c: &delivery.Question{Content: &store.Content{RepoSlug: "be", ContentID: "0001", Tags: "깨짐"}}})
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

// 실제 저장소 + 실제 라우터 + 실제 Picker: 경로 등록과 첫날, 같은 날, 다음 날, 복습 흐름을 끝까지 확인한다.
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
		Today: &delivery.Picker{
			Repos: store.NewRepoStore(conn), Contents: store.NewContentStore(conn), Questions: store.NewDailyQuestionStore(conn),
			Loc: kst, Now: func() time.Time { return now },
		},
	})
	titleNow := func() (int, string, bool) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/today", nil))
		var m struct {
			Item struct {
				Title  string `json:"title"`
				Review bool   `json:"review"`
			} `json:"item"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m.Item.Title, m.Item.Review
	}

	// 첫날: 아직 안 나온 새 글이 먼저 나옴
	if code, title, review := titleNow(); code != 200 || title != "첫 글" || review {
		t.Errorf("첫날: code=%d title=%q review=%v, want 200 첫 글 review=false", code, title, review)
	}

	// 하루 종일 같은 질문이고, 메일 발송 기록이나 로테이션이 바뀌어도 영향이 없음(발송과 무관)
	for _, q := range []string{
		`INSERT INTO delivery_log(subscriber_id, repo_slug, content_id, sent_at) VALUES (1, 'be', '0002', '2026-09-19 22:00:00')`,
		`UPDATE contents SET rotation_count = 5 WHERE content_id = '0001'`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now = time.Date(2026, 9, 20, 23, 59, 0, 0, kst)
	if code, title, review := titleNow(); code != 200 || title != "첫 글" || review {
		t.Errorf("같은 날 밤: code=%d title=%q review=%v, want 200 첫 글 review=false", code, title, review)
	}

	// 한국 자정이 지나면 다음 새 글
	now = time.Date(2026, 9, 21, 0, 0, 0, 0, kst)
	if code, title, review := titleNow(); code != 200 || title != "둘째 글" || review {
		t.Errorf("다음 날: code=%d title=%q review=%v, want 200 둘째 글 review=false", code, title, review)
	}

	// 새 글이 더 없으면 가장 오래전에 나온 글을 복습으로 보여 줌
	now = time.Date(2026, 9, 22, 9, 0, 0, 0, kst)
	if code, title, review := titleNow(); code != 200 || title != "첫 글" || !review {
		t.Errorf("셋째 날: code=%d title=%q review=%v, want 200 첫 글 review=true", code, title, review)
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
		Today: &delivery.Picker{
			Repos: store.NewRepoStore(conn), Contents: store.NewContentStore(conn), Questions: store.NewDailyQuestionStore(conn),
			Loc: time.UTC,
		},
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/today", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// 복습이면 응답의 review가 true다(웹이 "복습" 뱃지를 그리는 근거).
func TestToday_ReviewFlagIsReturned(t *testing.T) {
	rec := getToday(t, &fakePicker{c: &delivery.Question{
		Content: &store.Content{RepoSlug: "be", RepoName: "백엔드", ContentID: "0001", Title: "제목", Tags: `[]`},
		Review:  true,
	}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	var got struct {
		Item struct {
			Review bool `json:"review"`
		} `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Item.Review {
		t.Errorf("review = false, want true: %s", rec.Body)
	}
}
