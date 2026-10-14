package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	dbpkg "github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/store"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := dbpkg.Migrate(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func insertRepo(t *testing.T, db *sql.DB, slug, displayName string, active int) {
	t.Helper()
	mustExec(t, db,
		`INSERT INTO repos(slug, github_url, display_name, active) VALUES (?, ?, ?, ?)`,
		slug, "https://github.com/maeilham/"+slug, displayName, active)
}

func insertContent(t *testing.T, db *sql.DB, repoSlug, contentID, title string) {
	t.Helper()
	mustExec(t, db, `
		INSERT INTO contents(repo_slug, content_id, title, preview, tags, body_path, github_sha, discussion_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repoSlug, contentID, title, "preview-"+title, `["go","cache"]`,
		"content/"+contentID+"-x.md", "sha-"+repoSlug+"-"+contentID, "https://github.com/d/"+contentID)
}

// content_id는 repo 안에서만 유일하다. 같은 "0001"이 두 repo에 있어도 섞이면 안 된다.
func TestGetByRepoAndID_SameIDInDifferentRepos(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertRepo(t, db, "fe", "프론트엔드", 1)
	insertContent(t, db, "be", "0001", "백엔드 첫 글")
	insertContent(t, db, "fe", "0001", "프론트 첫 글")
	cs := store.NewContentStore(db)
	ctx := context.Background()

	be, err := cs.GetByRepoAndID(ctx, "be", "0001")
	if err != nil || be == nil {
		t.Fatalf("be/0001: c=%v err=%v", be, err)
	}
	fe, err := cs.GetByRepoAndID(ctx, "fe", "0001")
	if err != nil || fe == nil {
		t.Fatalf("fe/0001: c=%v err=%v", fe, err)
	}
	if be.Title != "백엔드 첫 글" || fe.Title != "프론트 첫 글" {
		t.Errorf("repo가 섞임: be=%q fe=%q", be.Title, fe.Title)
	}
}

func TestGetByRepoAndID_PopulatesFields(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertContent(t, db, "be", "0002", "제목")

	c, err := store.NewContentStore(db).GetByRepoAndID(context.Background(), "be", "0002")
	if err != nil || c == nil {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if c.RepoName != "백엔드" {
		t.Errorf("RepoName = %q, want repos.display_name", c.RepoName)
	}
	if c.GitHubURL != "https://github.com/maeilham/be" {
		t.Errorf("GitHubURL = %q", c.GitHubURL)
	}
	if c.BodyPath != "content/0002-x.md" || c.GithubSHA != "sha-be-0002" {
		t.Errorf("BodyPath=%q GithubSHA=%q (본문 조회 키로 쓰이는 값)", c.BodyPath, c.GithubSHA)
	}
	if c.Tags != `["go","cache"]` || c.DiscussionURL != "https://github.com/d/0002" {
		t.Errorf("Tags=%q DiscussionURL=%q", c.Tags, c.DiscussionURL)
	}
}

func TestGetByRepoAndID_NotFoundCases(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertRepo(t, db, "old", "옛 repo", 0) // 비활성
	insertContent(t, db, "be", "0001", "있음")
	insertContent(t, db, "be", "0002", "삭제됨")
	insertContent(t, db, "old", "0001", "비활성 repo의 글")
	mustExec(t, db, `UPDATE contents SET deleted_at = CURRENT_TIMESTAMP WHERE repo_slug='be' AND content_id='0002'`)
	cs := store.NewContentStore(db)

	cases := []struct{ name, repo, id string }{
		{"없는 id", "be", "9999"},
		{"없는 repo", "nope", "0001"},
		{"삭제된 글", "be", "0002"},
		{"비활성 repo", "old", "0001"},
	}
	for _, tc := range cases {
		c, err := cs.GetByRepoAndID(context.Background(), tc.repo, tc.id)
		if err != nil {
			t.Errorf("%s: err = %v, want (nil, nil)", tc.name, err)
		}
		if c != nil {
			t.Errorf("%s: c = %+v, want nil", tc.name, c)
		}
	}
}

func TestListSummaries_OrderFilterLimit(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertRepo(t, db, "fe", "프론트엔드", 1)
	insertRepo(t, db, "old", "옛 repo", 0) // 비활성
	for _, c := range [][2]string{{"be", "0001"}, {"be", "0002"}, {"be", "0003"}, {"be", "0004"}, {"be", "0005"}, {"fe", "0001"}, {"old", "0001"}} {
		insertContent(t, db, c[0], c[1], c[0]+"/"+c[1])
	}
	set := func(repo, id, col, val string) {
		mustExec(t, db, `UPDATE contents SET `+col+` = ? WHERE repo_slug = ? AND content_id = ?`, val, repo, id)
	}
	set("be", "0001", "authored_at", "2026-09-01 00:00:00")
	set("be", "0002", "authored_at", "2026-09-05 00:00:00")
	set("be", "0002", "sent_at", "2026-09-20 07:00:00")   // 최근에 발송됐어도 순서에는 영향이 없어야 함
	set("be", "0004", "synced_at", "2026-09-07 12:00:00") // authored_at이 NULL이면 synced_at으로 대체
	set("be", "0005", "authored_at", "2026-09-10 00:00:00")
	set("fe", "0001", "authored_at", "2026-09-10 00:00:00") // be/0005와 같은 시각: content_id 내림차순
	mustExec(t, db, `UPDATE contents SET deleted_at = CURRENT_TIMESTAMP WHERE repo_slug='be' AND content_id='0003'`)
	cs := store.NewContentStore(db)

	list, err := cs.ListSummaries(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range list {
		got = append(got, c.RepoSlug+"/"+c.ContentID)
	}
	// 작성일 최신순. 삭제·비활성 repo 제외. 같은 시각이면 content_id 내림차순(0005 > 0001).
	want := []string{"be/0005", "fe/0001", "be/0004", "be/0002", "be/0001"}
	if len(got) != len(want) {
		t.Fatalf("목록 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("목록 = %v, want %v", got, want)
		}
	}

	fmtT := func(tm time.Time) string { return tm.UTC().Format("2006-01-02 15:04:05") }
	if list[0].RepoName != "백엔드" || list[0].Tags != `["go","cache"]` || list[0].Preview == "" {
		t.Errorf("필드가 채워지지 않음: %+v", list[0])
	}
	if fmtT(list[0].AuthoredAt) != "2026-09-10 00:00:00" {
		t.Errorf("AuthoredAt = %v", list[0].AuthoredAt)
	}
	if fmtT(list[2].AuthoredAt) != "2026-09-07 12:00:00" {
		t.Errorf("authored_at이 없으면 synced_at으로 대체돼야 함: %v", list[2].AuthoredAt)
	}
	if fmtT(list[3].SentAt) != "2026-09-20 07:00:00" || !list[0].SentAt.IsZero() {
		t.Errorf("SentAt: 발송 글=%v, 미발송 글=%v", list[3].SentAt, list[0].SentAt)
	}

	limited, err := cs.ListSummaries(context.Background(), 2)
	if err != nil || len(limited) != 2 || limited[0].ContentID != "0005" || limited[1].RepoSlug != "fe" {
		t.Errorf("limit=2: len=%d err=%v", len(limited), err)
	}
}

func TestListSummaries_Empty(t *testing.T) {
	list, err := store.NewContentStore(newTestDB(t)).ListSummaries(context.Background(), 50)
	if err != nil || len(list) != 0 {
		t.Errorf("빈 DB: len=%d err=%v", len(list), err)
	}
}

func TestSetAuthoredAt_OnlyWhenEmpty(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertContent(t, db, "be", "0001", "글")
	cs := store.NewContentStore(db)
	ctx := context.Background()

	// 처음에는 비어 있다 (ListByRepo는 저장된 값 그대로 돌려준다)
	rows, err := cs.ListByRepo(ctx, "be")
	if err != nil || len(rows) != 1 || !rows[0].AuthoredAt.IsZero() {
		t.Fatalf("초기 상태: rows=%v err=%v", rows, err)
	}

	// 한국 시간으로 줘도 UTC로 저장된다
	kst := time.FixedZone("KST", 9*3600)
	if err := cs.SetAuthoredAt(ctx, "be", "0001", time.Date(2026, 9, 1, 9, 0, 0, 0, kst)); err != nil {
		t.Fatal(err)
	}
	rows, _ = cs.ListByRepo(ctx, "be")
	if got := rows[0].AuthoredAt.UTC().Format("2006-01-02 15:04:05"); got != "2026-09-01 00:00:00" {
		t.Errorf("AuthoredAt = %s, want 2026-09-01 00:00:00 (UTC)", got)
	}

	// 이미 채워졌으면 덮어쓰지 않는다 (최초 커밋 시각은 바뀌지 않음)
	if err := cs.SetAuthoredAt(ctx, "be", "0001", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	rows, _ = cs.ListByRepo(ctx, "be")
	if got := rows[0].AuthoredAt.UTC().Format("2006-01-02"); got != "2026-09-01" {
		t.Errorf("덮어써짐: %s", got)
	}
}
