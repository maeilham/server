package delivery

import (
	"database/sql"
	"testing"
	"time"

	dbpkg "github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/store"
)

// 서비스 시간대(운영 기본값 Asia/Seoul)와 같은 +09:00이다.
var kst = time.FixedZone("KST", 9*3600)

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

// pickerAt은 실제 저장소와 고정된 기준 시각으로 Picker를 만든다(서비스 시간대는 KST).
func pickerAt(db *sql.DB, now time.Time) *Picker {
	return &Picker{
		Repos:    store.NewRepoStore(db),
		Subs:     store.NewSubscriberStore(db),
		Contents: store.NewContentStore(db),
		Log:      store.NewDeliveryLogStore(db),
		Loc:      kst,
		Now:      func() time.Time { return now },
	}
}

func newStores(db *sql.DB) (store.SubscriberRepository, store.ContentRepository) {
	return store.NewSubscriberStore(db), store.NewContentStore(db)
}
