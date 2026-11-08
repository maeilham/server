package content

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/maeilham/server/internal/store"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeCreator struct {
	categories map[string]string
	failTitle  string // 이 제목을 가진 글은 생성에 실패함
	metaCalls  int
	created    []string // 요청한 category ID
	titles     []string
}

func (f *fakeCreator) RepoMeta(context.Context, string, string) (string, map[string]string, error) {
	f.metaCalls++
	return "repo-id", f.categories, nil
}

func (f *fakeCreator) CreateDiscussion(_ context.Context, owner, repo, repoID, categoryID, title, body string) (string, string, error) {
	if f.failTitle != "" && title == "[매일함] "+f.failTitle {
		return "", "", errors.New("boom")
	}
	f.created = append(f.created, categoryID)
	f.titles = append(f.titles, title)
	n := len(f.created)
	return "https://github.com/" + owner + "/" + repo + "/discussions/" + string(rune('0'+n)), "node-" + string(rune('0'+n)), nil
}

func seedContents(t *testing.T, ids ...string) (store.ContentRepository, *store.Repo, func(id string) (url, node string)) {
	t.Helper()
	conn := newSyncDB(t)
	if _, err := conn.Exec(`INSERT OR IGNORE INTO repos(slug, github_url, display_name) VALUES ('be', 'https://github.com/maeilham/be', 'be')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := conn.Exec(`INSERT INTO contents(repo_slug, content_id, title, preview, body_path) VALUES ('be', ?, ?, 'p', ?)`, id, "t"+id, "content/"+id+".md"); err != nil {
			t.Fatal(err)
		}
	}
	read := func(id string) (string, string) {
		var url, node string
		if err := conn.QueryRow(`SELECT COALESCE(discussion_url,''), COALESCE(discussion_node_id,'') FROM contents WHERE content_id = ?`, id).Scan(&url, &node); err != nil {
			t.Fatal(err)
		}
		return url, node
	}
	return store.NewContentStore(conn), &store.Repo{Slug: "be", GitHubURL: "https://github.com/maeilham/be"}, read
}

func TestEnsureDiscussions_CreatesOnlyMissingAndSavesThem(t *testing.T) {
	cs, repo, read := seedContents(t, "0001", "0002", "0003")
	if err := cs.SaveDiscussionURL(context.Background(), "be", "0002", "https://github.com/maeilham/be/discussions/99", ""); err != nil {
		t.Fatal(err)
	}
	fc := &fakeCreator{categories: map[string]string{"General": "cat-general"}}

	created, failed, err := EnsureDiscussions(context.Background(), discardLogger(), fc, cs, repo)
	if err != nil || created != 2 || failed != 0 {
		t.Fatalf("created=%d failed=%d err=%v, want 2/0/nil", created, failed, err)
	}
	if u, n := read("0001"); u == "" || n == "" {
		t.Errorf("0001 not saved: url=%q node=%q", u, n)
	}
	if u, _ := read("0002"); u != "https://github.com/maeilham/be/discussions/99" {
		t.Errorf("0002 already had a Discussion (URL만, node ID 없음) but was overwritten: %q", u)
	}
	if len(fc.titles) != 2 || fc.titles[0] != "[매일함] t0001" {
		t.Errorf("titles = %v", fc.titles)
	}

	// 다시 돌리면 만들 게 없고, GitHub 메타도 조회하지 않는다
	fc.metaCalls = 0
	created, _, _ = EnsureDiscussions(context.Background(), discardLogger(), fc, cs, repo)
	if created != 0 || fc.metaCalls != 0 {
		t.Errorf("rerun: created=%d metaCalls=%d, want 0 and 0", created, fc.metaCalls)
	}
}

func TestEnsureDiscussions_OneFailureDoesNotStopOthersAndIsRetried(t *testing.T) {
	cs, repo, read := seedContents(t, "0001", "0002", "0003")
	fc := &fakeCreator{categories: map[string]string{"General": "c"}, failTitle: "t0002"}

	created, failed, err := EnsureDiscussions(context.Background(), discardLogger(), fc, cs, repo)
	if err != nil || created != 2 || failed != 1 {
		t.Fatalf("created=%d failed=%d err=%v, want 2/1/nil", created, failed, err)
	}
	if u, _ := read("0002"); u != "" {
		t.Errorf("failed content must stay without a Discussion, got %q", u)
	}

	fc.failTitle = ""
	created, failed, _ = EnsureDiscussions(context.Background(), discardLogger(), fc, cs, repo)
	if created != 1 || failed != 0 {
		t.Errorf("retry: created=%d failed=%d, want only 0002 created", created, failed)
	}
	if u, _ := read("0002"); u == "" {
		t.Error("0002 should have a Discussion after retry")
	}
}

func TestEnsureDiscussions_NoMissingMeansNoGitHubCall(t *testing.T) {
	cs, repo, _ := seedContents(t)
	fc := &fakeCreator{}
	if _, _, err := EnsureDiscussions(context.Background(), discardLogger(), fc, cs, repo); err != nil || fc.metaCalls != 0 {
		t.Errorf("err=%v metaCalls=%d, want no call when nothing is missing", err, fc.metaCalls)
	}
}

func TestEnsureDiscussions_NoCategoryIsAnError(t *testing.T) {
	cs, repo, _ := seedContents(t, "0001")
	_, _, err := EnsureDiscussions(context.Background(), discardLogger(), &fakeCreator{categories: map[string]string{}}, cs, repo)
	if err == nil {
		t.Fatal("err = nil, want an error when the repo has no Discussion category")
	}
}

func TestPickCategory(t *testing.T) {
	cats := map[string]string{"Q&A": "qa", "Ideas": "ideas", "General": "general"}
	if got := pickCategory("configured", cats); got != "configured" {
		t.Errorf("configured: got %q", got)
	}
	if got := pickCategory("", cats); got != "general" {
		t.Errorf("General preferred: got %q", got)
	}
	delete(cats, "General")
	for i := 0; i < 20; i++ { // 맵 순회 순서와 무관하게 이름순 첫 카테고리
		if got := pickCategory("", cats); got != "ideas" {
			t.Fatalf("name-sorted first: got %q", got)
		}
	}
	if got := pickCategory("", nil); got != "" {
		t.Errorf("none: got %q", got)
	}
}
