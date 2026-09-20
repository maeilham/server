package store_test

import (
	"context"
	"database/sql"
	"testing"

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
