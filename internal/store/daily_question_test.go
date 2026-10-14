package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	dbpkg "github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/store"
)

// day는 2026-10월의 i일을 서비스의 날짜 문자열로 만듦.
func day(i int) string { return fmt.Sprintf("2026-10-%02d", i) }

func ensure(t *testing.T, dq store.DailyQuestionRepository, day, repo string) *store.DailyQuestion {
	t.Helper()
	q, err := dq.Ensure(context.Background(), day, repo)
	if err != nil {
		t.Fatalf("Ensure(%s, %s): %v", day, repo, err)
	}
	return q
}

// 글이 모두 한 번씩 나올 때까지는 새 글이고, 그 뒤에는 가장 오래전에 나온 글을 복습으로 돌려서 content_id 순으로 순환함.
func TestEnsure_NewFirstThenOldestAsReview(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertContent(t, db, "be", "0001", "a")
	insertContent(t, db, "be", "0002", "b")
	dq := store.NewDailyQuestionStore(db)

	want := []struct {
		id     string
		review bool
	}{
		{"0001", false}, {"0002", false}, // 새 글
		{"0001", true}, {"0002", true}, {"0001", true}, // 가장 오래전에 나온 글 순환
	}
	for i, w := range want {
		q := ensure(t, dq, day(i+1), "be")
		if q == nil || q.ContentID != w.id || q.Review != w.review {
			t.Fatalf("day %d: got %+v, want %s review=%v", i+1, q, w.id, w.review)
		}
	}
}

func TestEnsure_IsIdempotentAndStableWithinTheDay(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertContent(t, db, "be", "0001", "a")
	dq := store.NewDailyQuestionStore(db)

	first := ensure(t, dq, day(1), "be")
	// 같은 날 새 글이 병합돼도 그날의 질문은 바뀌지 않음
	insertContent(t, db, "be", "0002", "b")
	again := ensure(t, dq, day(1), "be")
	if first.ContentID != "0001" || *again != *first {
		t.Errorf("same day changed: first=%+v again=%+v", first, again)
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM daily_question`).Scan(&rows)
	if rows != 1 {
		t.Errorf("rows = %d, want 1", rows)
	}
	// 다음 날에는 새 글이 먼저 나옴(복습보다 우선)
	if q := ensure(t, dq, day(2), "be"); q.ContentID != "0002" || q.Review {
		t.Errorf("next day: got %+v, want new content 0002", q)
	}
}

func TestEnsure_RepoWithoutContentStoresNothing(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "empty", "빈 repo", 1)
	dq := store.NewDailyQuestionStore(db)

	if q := ensure(t, dq, day(1), "empty"); q != nil {
		t.Errorf("got %+v, want nil", q)
	}
	if q, err := dq.Get(context.Background(), day(1), "empty"); err != nil || q != nil {
		t.Errorf("Get = %+v err=%v, want nothing stored", q, err)
	}
}

func TestEnsure_SkipsDeletedContent(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertContent(t, db, "be", "0001", "a")
	insertContent(t, db, "be", "0002", "b")
	mustExec(t, db, `UPDATE contents SET deleted_at = CURRENT_TIMESTAMP WHERE content_id = '0001'`)
	dq := store.NewDailyQuestionStore(db)

	if q := ensure(t, dq, day(1), "be"); q == nil || q.ContentID != "0002" {
		t.Errorf("got %+v, want 0002 (0001 is deleted)", q)
	}
	mustExec(t, db, `UPDATE contents SET deleted_at = CURRENT_TIMESTAMP`)
	if q := ensure(t, dq, day(2), "be"); q != nil {
		t.Errorf("all deleted: got %+v, want nil", q)
	}
}

// repo마다 따로 정함: be가 복습 중이어도 fe는 새 글을 냄.
func TestEnsure_ReposAreIndependent(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertRepo(t, db, "fe", "프론트엔드", 1)
	insertContent(t, db, "be", "0001", "a")
	insertContent(t, db, "fe", "0001", "x")
	insertContent(t, db, "fe", "0002", "y")
	dq := store.NewDailyQuestionStore(db)

	ensure(t, dq, day(1), "be")
	ensure(t, dq, day(1), "fe")
	be := ensure(t, dq, day(2), "be") // be는 글이 하나라 복습
	fe := ensure(t, dq, day(2), "fe") // fe는 아직 안 나온 새 글
	if !be.Review || be.ContentID != "0001" {
		t.Errorf("be: %+v, want review 0001", be)
	}
	if fe.Review || fe.ContentID != "0002" {
		t.Errorf("fe: %+v, want new 0002", fe)
	}
}

// 저장된 글이 그 뒤에 삭제되면 다시 정해서 같은 행을 갱신함.
func TestEnsure_RepicksWhenStoredContentIsDeleted(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertContent(t, db, "be", "0001", "a")
	insertContent(t, db, "be", "0002", "b")
	dq := store.NewDailyQuestionStore(db)

	if q := ensure(t, dq, day(1), "be"); q.ContentID != "0001" {
		t.Fatalf("setup: got %+v", q)
	}
	mustExec(t, db, `UPDATE contents SET deleted_at = CURRENT_TIMESTAMP WHERE content_id = '0001'`)

	q := ensure(t, dq, day(1), "be")
	if q == nil || q.ContentID != "0002" || q.Review {
		t.Errorf("got %+v, want 0002 as a new question", q)
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM daily_question`).Scan(&rows)
	if rows != 1 {
		t.Errorf("rows = %d, want the same row updated", rows)
	}
}

// 복습 후보는 day보다 이전에 나온 기록만 봄: 미래 날짜의 행이 있어도 과거 날짜의 선정에 영향이 없음.
func TestEnsure_OnlyLooksAtEarlierDays(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertContent(t, db, "be", "0001", "a")
	insertContent(t, db, "be", "0002", "b")
	dq := store.NewDailyQuestionStore(db)

	ensure(t, dq, day(20), "be") // 먼저 20일을 정함: 0001
	q := ensure(t, dq, day(10), "be")
	if q.ContentID != "0001" || q.Review {
		t.Errorf("day 10: got %+v, want 0001 as new (20일의 기록은 보지 않음)", q)
	}
}

func TestGet_ReturnsStoredRowOnly(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertContent(t, db, "be", "0001", "a")
	dq := store.NewDailyQuestionStore(db)

	if q, err := dq.Get(context.Background(), day(1), "be"); err != nil || q != nil {
		t.Fatalf("before Ensure: %+v err=%v, want nothing", q, err)
	}
	want := ensure(t, dq, day(1), "be")
	if got, err := dq.Get(context.Background(), day(1), "be"); err != nil || got == nil || *got != *want {
		t.Errorf("after Ensure: %+v err=%v, want %+v", got, err, want)
	}
}

// 같은 (day, repo)를 동시에 정해도 행이 하나이고 모두 같은 질문을 받음. 충돌이 실제로 일어나도록 여러 날에 걸쳐
// 모든 요청을 한꺼번에 출발시킴. :memory:는 연결마다 DB가 달라서 파일 DB를 씀.
func TestEnsure_ConcurrentCallsAgree(t *testing.T) {
	conn, err := dbpkg.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := dbpkg.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	insertRepo(t, conn, "be", "백엔드", 1)
	for _, id := range []string{"0001", "0002", "0003"} {
		insertContent(t, conn, "be", id, id)
	}
	dq := store.NewDailyQuestionStore(conn)

	const days, workers = 30, 16
	for d := 1; d <= days; d++ {
		got := make([]string, workers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start // 모두 같은 순간에 출발
				q, err := dq.Ensure(context.Background(), day(d), "be")
				if err != nil || q == nil {
					got[i] = fmt.Sprintf("error: %v", err)
					return
				}
				got[i] = q.ContentID
			}(i)
		}
		close(start)
		wg.Wait()
		for i := range got {
			if got[i] != got[0] {
				t.Fatalf("day %d: results differ: %v", d, got)
			}
		}
		if strings.HasPrefix(got[0], "error") {
			t.Fatalf("day %d: %v", d, got)
		}
	}
	var rows int
	_ = conn.QueryRow(`SELECT COUNT(*) FROM daily_question`).Scan(&rows)
	if rows != days {
		t.Errorf("rows = %d, want one per day (%d)", rows, days)
	}
}

// 스키마 제약: (day, repo)는 하나뿐이고, review는 0/1만, 없는 글을 가리킬 수 없음.
func TestDailyQuestionSchemaConstraints(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertContent(t, db, "be", "0001", "a")

	ins := func(day, repo, content string, review int) error {
		_, err := db.Exec(`INSERT INTO daily_question(day, repo_slug, content_id, review) VALUES (?, ?, ?, ?)`, day, repo, content, review)
		return err
	}
	if err := ins(day(1), "be", "0001", 0); err != nil {
		t.Fatal(err)
	}
	if err := ins(day(1), "be", "0001", 0); err == nil {
		t.Error("duplicate (day, repo) must be rejected")
	}
	if err := ins(day(2), "be", "0001", 2); err == nil {
		t.Error("review outside 0/1 must be rejected")
	}
	if err := ins(day(3), "be", "9999", 0); err == nil {
		t.Error("a row pointing to a missing content must be rejected")
	}
}

// 나온 기록은 repo별로 셈: 다른 repo에 같은 content_id가 나왔다고 이 repo의 글이 복습이 되면 안 됨.
func TestEnsure_HistoryIsPerRepo(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertRepo(t, db, "fe", "프론트엔드", 1)
	insertContent(t, db, "be", "0001", "a")
	insertContent(t, db, "fe", "0001", "x") // be와 같은 content_id
	dq := store.NewDailyQuestionStore(db)

	ensure(t, dq, day(1), "be") // be의 0001만 나옴
	q := ensure(t, dq, day(2), "fe")
	if q == nil || q.ContentID != "0001" || q.Review {
		t.Errorf("fe: got %+v, want its own 0001 as a new question", q)
	}
}
