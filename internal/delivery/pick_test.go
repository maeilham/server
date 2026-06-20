package delivery

import (
	"context"
	"database/sql"
	"testing"
	"time"

	dbpkg "github.com/maeilham/server/internal/db"
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

func insertConfirmedSubscriber(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO subscribers(email, confirmed_at) VALUES (?, CURRENT_TIMESTAMP)`, email)
	if err != nil {
		t.Fatalf("insert subscriber: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func insertRepo(t *testing.T, db *sql.DB, slug string) {
	t.Helper()
	mustExec(t, db,
		`INSERT INTO repos(slug, github_url, display_name) VALUES (?, ?, ?)`,
		slug, "https://github.com/maeilham/"+slug, slug)
}

func insertContent(t *testing.T, db *sql.DB, repoSlug, contentID string) {
	t.Helper()
	mustExec(t, db, `
		INSERT INTO contents(repo_slug, content_id, title, preview, body_path)
		VALUES (?, ?, ?, ?, ?)`,
		repoSlug, contentID, "title-"+contentID, "preview-"+contentID,
		"content/"+contentID+".md")
}

func subscribe(t *testing.T, db *sql.DB, subID int64, repoSlug string, weight int) {
	t.Helper()
	mustExec(t, db,
		`INSERT INTO subscriptions(subscriber_id, repo_slug, weight) VALUES (?, ?, ?)`,
		subID, repoSlug, weight)
}

// ---------------- TodayContentForRepo ----------------

func TestTodayContentForRepo_Empty(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")

	got, err := TodayContentForRepo(context.Background(), db, "be")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty repo, got %+v", got)
	}
}

func TestTodayContentForRepo_OrdersByRotationThenContentID(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	insertContent(t, db, "be", "0003")

	// 0001 already rotated, so 0002 should now be "today"
	mustExec(t, db, `UPDATE contents SET rotation_count = 1 WHERE content_id = '0001'`)

	got, err := TodayContentForRepo(context.Background(), db, "be")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got == nil || got.ContentID != "0002" {
		t.Errorf("expected 0002, got %+v", got)
	}
}

func TestTodayContentForRepo_IgnoresDeleted(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	mustExec(t, db, `UPDATE contents SET deleted_at = CURRENT_TIMESTAMP WHERE content_id = '0001'`)

	got, err := TodayContentForRepo(context.Background(), db, "be")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got == nil || got.ContentID != "0002" {
		t.Errorf("expected 0002 (0001 is deleted), got %+v", got)
	}
}

// ---------------- PickForSubscriber ----------------

func TestPickForSubscriber_Unconfirmed(t *testing.T) {
	db := newTestDB(t)
	res, _ := db.Exec(`INSERT INTO subscribers(email) VALUES (?)`, "x@x.kr")
	id, _ := res.LastInsertId()
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	subscribe(t, db, id, "be", 3)

	got, err := PickForSubscriber(context.Background(), db, id, time.Now())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("unconfirmed subscriber should not receive: %+v", got)
	}
}

func TestPickForSubscriber_Paused(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "x@x.kr")
	mustExec(t, db, `UPDATE subscribers SET paused_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	subscribe(t, db, id, "be", 3)

	got, err := PickForSubscriber(context.Background(), db, id, time.Now())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("paused subscriber should not receive: %+v", got)
	}
}

func TestPickForSubscriber_NoSubscriptions(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "x@x.kr")

	got, err := PickForSubscriber(context.Background(), db, id, time.Now())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("no-subscription subscriber should not receive: %+v", got)
	}
}

func TestPickForSubscriber_NoEligibleContent(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "x@x.kr")
	insertRepo(t, db, "be") // no contents
	subscribe(t, db, id, "be", 3)

	got, err := PickForSubscriber(context.Background(), db, id, time.Now())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when no contents exist, got %+v", got)
	}
}

func TestPickForSubscriber_SingleSubscription(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "x@x.kr")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	subscribe(t, db, id, "be", 3)

	got, err := PickForSubscriber(context.Background(), db, id, time.Now())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got == nil || got.RepoSlug != "be" || got.ContentID != "0001" {
		t.Errorf("expected be/0001, got %+v", got)
	}
}

func TestPickForSubscriber_IgnoresInactiveRepo(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "x@x.kr")
	insertRepo(t, db, "be")
	insertRepo(t, db, "fe")
	mustExec(t, db, `UPDATE repos SET active = 0 WHERE slug = 'be'`)
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "fe", "0001")
	subscribe(t, db, id, "be", 5)
	subscribe(t, db, id, "fe", 1)

	got, err := PickForSubscriber(context.Background(), db, id, time.Now())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got == nil || got.RepoSlug != "fe" {
		t.Errorf("inactive repo should be skipped; got %+v", got)
	}
}

func TestPickForSubscriber_DeterministicSameDay(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "x@x.kr")
	insertRepo(t, db, "be")
	insertRepo(t, db, "fe")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "fe", "0001")
	subscribe(t, db, id, "be", 3)
	subscribe(t, db, id, "fe", 3)

	day := time.Date(2026, 5, 24, 0, 0, 0, 0, time.UTC)
	first, _ := PickForSubscriber(context.Background(), db, id, day)
	for range 5 {
		got, _ := PickForSubscriber(context.Background(), db, id, day)
		if got.RepoSlug != first.RepoSlug || got.ContentID != first.ContentID {
			t.Fatalf("not deterministic: first=%v, retry=%v", first, got)
		}
	}
}

func TestPickForSubscriber_DifferentDayMayDiffer(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "x@x.kr")
	insertRepo(t, db, "be")
	insertRepo(t, db, "fe")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "fe", "0001")
	subscribe(t, db, id, "be", 3)
	subscribe(t, db, id, "fe", 3)

	// 다양한 날짜를 시도해서 적어도 한 번은 다른 repo가 뽑히는지 확인
	day0 := time.Date(2026, 5, 24, 0, 0, 0, 0, time.UTC)
	first, _ := PickForSubscriber(context.Background(), db, id, day0)
	differs := false
	for i := 1; i <= 60; i++ {
		got, _ := PickForSubscriber(context.Background(), db, id, day0.AddDate(0, 0, i))
		if got.RepoSlug != first.RepoSlug {
			differs = true
			break
		}
	}
	if !differs {
		t.Errorf("expected pick to differ across days; same repo every day for 60 days")
	}
}

func TestPickForSubscriber_WeightDistribution(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertRepo(t, db, "fe")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "fe", "0001")

	// 충분히 많은 구독자를 만들어 다른 시드로 분포 검증
	beCount := 0
	feCount := 0
	const n = 1000
	for i := range n {
		id := insertConfirmedSubscriber(t, db, "u"+stringFromInt(i)+"@x.kr")
		subscribe(t, db, id, "be", 5) // be weight 5
		subscribe(t, db, id, "fe", 1) // fe weight 1
		got, _ := PickForSubscriber(context.Background(), db, id, time.Now())
		switch got.RepoSlug {
		case "be":
			beCount++
		case "fe":
			feCount++
		}
	}
	// 기대치: be ≈ 5/6 ≈ 833, fe ≈ 1/6 ≈ 167. 통계적 변동성을 감안해 넉넉히.
	if beCount < 750 || beCount > 900 {
		t.Errorf("be picks out of expected range: got %d / %d", beCount, n)
	}
	if feCount < 100 || feCount > 250 {
		t.Errorf("fe picks out of expected range: got %d / %d", feCount, n)
	}
}

func stringFromInt(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = digits[i%10]
		i /= 10
	}
	return string(buf[pos:])
}
