package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	dbpkg "github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/store"
)

// ── 가짜 GitHub ──────────────────────────────────────────────────────────────

type fakeFile struct {
	sha  string
	raw  string
	date string // 이 파일의 최초 커밋 시각(RFC3339)
}

// fakeGitHub는 sync가 부르는 세 가지 엔드포인트(트리, 원문, 커밋 목록)를 흉내낸다.
type fakeGitHub struct {
	srv         *httptest.Server
	mu          sync.Mutex
	files       map[string]*fakeFile // key: content/0001-a.md
	commitsFail bool
	rawCalls    int
	commitCalls int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{files: map[string]*fakeFile{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.Contains(r.URL.Path, "/git/trees/"):
			var tree []map[string]any
			for path, file := range f.files {
				tree = append(tree, map[string]any{"path": path, "type": "blob", "sha": file.sha, "size": len(file.raw)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tree": tree})
		case strings.HasSuffix(r.URL.Path, "/commits"):
			f.commitCalls++
			if f.commitsFail {
				http.Error(w, `{"message":"rate limited"}`, http.StatusForbidden)
				return
			}
			file := f.files[r.URL.Query().Get("path")]
			if file == nil {
				fmt.Fprint(w, "[]")
				return
			}
			fmt.Fprintf(w, `[{"commit":{"author":{"date":%q}}}]`, file.date)
		default: // 원문: /{owner}/{repo}/main/{path}
			f.rawCalls++
			path := strings.TrimPrefix(r.URL.Path, "/maeilham/backend-ops/main/")
			file := f.files[path]
			if file == nil {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, file.raw)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) client() *GitHubClient {
	return &GitHubClient{HTTP: f.srv.Client(), APIBase: f.srv.URL, RawBase: f.srv.URL}
}

func (f *fakeGitHub) counts() (raw, commits int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rawCalls, f.commitCalls
}

func rawFile(title string) string {
	return fmt.Sprintf("---\ntitle: %q\npreview: \"미리보기\"\ntags: [go]\n---\n\n본문\n", title)
}

// ── 준비 도구 ────────────────────────────────────────────────────────────────

const testRepoURL = "https://github.com/maeilham/backend-ops"

func newSyncDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Exec(`INSERT INTO repos(slug, github_url, display_name) VALUES ('be', ?, '백엔드')`, testRepoURL); err != nil {
		t.Fatal(err)
	}
	return conn
}

func runSync(t *testing.T, conn *sql.DB, gh *fakeGitHub) *SyncStats {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stats, err := Sync(context.Background(), logger, store.NewContentStore(conn), gh.client(), nil, "be", testRepoURL, "")
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return stats
}

// authoredAt은 DB에 저장된 원본 텍스트를 돌려준다. NULL이면 "".
// TIMESTAMP 컬럼을 그냥 읽으면 드라이버가 시각으로 해석해 형식을 바꾸므로 CAST로 저장된 그대로 읽는다.
func authoredAt(t *testing.T, conn *sql.DB, contentID string) string {
	t.Helper()
	var v sql.NullString
	if err := conn.QueryRow(`SELECT CAST(authored_at AS TEXT) FROM contents WHERE repo_slug='be' AND content_id=?`, contentID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v.String
}

// ── 테스트 ───────────────────────────────────────────────────────────────────

func TestSync_NewFilesGetAuthoredAt(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.files["content/0001-a.md"] = &fakeFile{sha: "sha-a", raw: rawFile("A"), date: "2026-08-01T09:00:00+09:00"}
	gh.files["content/0002-b.md"] = &fakeFile{sha: "sha-b", raw: rawFile("B"), date: "2026-09-05T00:00:00Z"}
	conn := newSyncDB(t)

	stats := runSync(t, conn, gh)
	if stats.Inserted != 2 || stats.Authored != 2 || stats.AuthoredFailed != 0 || stats.Errors != 0 {
		t.Errorf("stats = %+v", stats)
	}
	if got := authoredAt(t, conn, "0001"); got != "2026-08-01 00:00:00" {
		t.Errorf("0001 authored_at = %q, want 2026-08-01 00:00:00 (UTC로 저장)", got)
	}
	if got := authoredAt(t, conn, "0002"); got != "2026-09-05 00:00:00" {
		t.Errorf("0002 authored_at = %q", got)
	}
}

// 작성일 컬럼이 생기기 전부터 있던 글(SHA는 같고 authored_at만 NULL)도 채워져야 한다.
// 원문은 다시 받지 않아야 한다.
func TestSync_BackfillsExistingContentWithoutRefetchingBody(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.files["content/0001-a.md"] = &fakeFile{sha: "sha-a", raw: rawFile("A"), date: "2026-08-01T00:00:00Z"}
	conn := newSyncDB(t)
	if _, err := conn.Exec(`INSERT INTO contents(repo_slug, content_id, title, preview, body_path, github_sha)
		VALUES ('be','0001','A','p','content/0001-a.md','sha-a')`); err != nil {
		t.Fatal(err)
	}

	stats := runSync(t, conn, gh)
	if stats.Inserted != 0 || stats.Updated != 0 || stats.Authored != 1 {
		t.Errorf("stats = %+v", stats)
	}
	if raw, _ := gh.counts(); raw != 0 {
		t.Errorf("원문 요청 %d번, want 0 (SHA가 같으면 본문은 다시 받지 않음)", raw)
	}
	if got := authoredAt(t, conn, "0001"); got != "2026-08-01 00:00:00" {
		t.Errorf("authored_at = %q", got)
	}

	// 이미 채워졌으니 다음 sync는 커밋 API를 다시 부르지 않는다.
	_, before := gh.counts()
	runSync(t, conn, gh)
	if _, after := gh.counts(); after != before {
		t.Errorf("이미 채워진 글에 커밋 API를 다시 호출함 (%d -> %d)", before, after)
	}
}

// GitHub 커밋 API가 실패해도 sync는 성공하고, 다음 sync가 다시 시도해 채운다.
func TestSync_CommitAPIFailureDoesNotFailSyncAndRetries(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.files["content/0001-a.md"] = &fakeFile{sha: "sha-a", raw: rawFile("A"), date: "2026-08-01T00:00:00Z"}
	gh.commitsFail = true
	conn := newSyncDB(t)

	stats := runSync(t, conn, gh)
	if stats.Inserted != 1 || stats.Authored != 0 || stats.AuthoredFailed != 1 || stats.Errors != 0 {
		t.Errorf("stats = %+v (글은 저장되고 작성일만 비어야 함)", stats)
	}
	if got := authoredAt(t, conn, "0001"); got != "" {
		t.Errorf("authored_at = %q, want NULL", got)
	}

	gh.mu.Lock()
	gh.commitsFail = false
	gh.mu.Unlock()
	rawBefore, _ := gh.counts()

	stats = runSync(t, conn, gh)
	if stats.Authored != 1 {
		t.Errorf("재시도 stats = %+v", stats)
	}
	if got := authoredAt(t, conn, "0001"); got != "2026-08-01 00:00:00" {
		t.Errorf("재시도 후 authored_at = %q", got)
	}
	if rawAfter, _ := gh.counts(); rawAfter != rawBefore {
		t.Errorf("재시도에서 원문을 다시 받음 (%d -> %d)", rawBefore, rawAfter)
	}
}

// 글이 수정돼도(SHA 변경) 작성일은 그대로다. 커밋 API도 다시 부르지 않는다.
func TestSync_EditedContentKeepsAuthoredAt(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.files["content/0001-a.md"] = &fakeFile{sha: "sha-1", raw: rawFile("A"), date: "2026-08-01T00:00:00Z"}
	conn := newSyncDB(t)
	runSync(t, conn, gh)

	gh.mu.Lock()
	gh.files["content/0001-a.md"] = &fakeFile{sha: "sha-2", raw: rawFile("A 수정"), date: "2026-09-19T00:00:00Z"}
	gh.mu.Unlock()
	_, commitsBefore := gh.counts()

	stats := runSync(t, conn, gh)
	if stats.Updated != 1 || stats.Authored != 0 {
		t.Errorf("stats = %+v", stats)
	}
	if got := authoredAt(t, conn, "0001"); got != "2026-08-01 00:00:00" {
		t.Errorf("수정 후 authored_at = %q, 최초 시각이 유지돼야 함", got)
	}
	if _, commitsAfter := gh.counts(); commitsAfter != commitsBefore {
		t.Errorf("수정된 글에 커밋 API를 다시 호출함 (%d -> %d)", commitsBefore, commitsAfter)
	}
	var title string
	_ = conn.QueryRow(`SELECT title FROM contents WHERE content_id='0001'`).Scan(&title)
	if title != "A 수정" {
		t.Errorf("제목이 갱신되지 않음: %q", title)
	}
}
