package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/maeilham/server/internal/store"
)

func insertSubscriber(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO subscribers(email, confirmed_at) VALUES ('a@example.com', CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// delivery_log에는 Record가 저장하는 형식(UTC, "YYYY-MM-DD HH:MM:SS")으로 넣는다.
func insertDelivery(t *testing.T, db *sql.DB, subID int64, repo, contentID, sentAtUTC string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO delivery_log(subscriber_id, repo_slug, content_id, sent_at) VALUES (?, ?, ?, ?)`,
		subID, repo, contentID, sentAtUTC)
}

// 서비스의 하루는 [from, to)이고 시작은 포함, 끝은 제외다. sent_at은 UTC 문자열이라 구간도 UTC로 바꿔 비교한다.
func TestAlreadySent_ServiceDayInterval(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertContent(t, db, "be", "0001", "t")
	sub := insertSubscriber(t, db)
	other := insertSubscriber2(t, db)
	ls := store.NewDeliveryLogStore(db)
	ctx := context.Background()

	kst := time.FixedZone("KST", 9*3600)
	from := time.Date(2026, 9, 20, 0, 0, 0, 0, kst) // 2026-09-19 15:00:00 UTC
	to := from.AddDate(0, 0, 1)                     // 2026-09-20 15:00:00 UTC

	sentAt := func(subID int64, utcStr string) bool {
		t.Helper()
		mustExec(t, db, `DELETE FROM delivery_log`)
		insertDelivery(t, db, subID, "be", "0001", utcStr)
		got, err := ls.AlreadySent(ctx, sub, from, to)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if sentAt(sub, "2026-09-19 14:59:59") {
		t.Error("one second before the interval must not count")
	}
	if !sentAt(sub, "2026-09-19 15:00:00") {
		t.Error("the start of the interval must count")
	}
	if !sentAt(sub, "2026-09-20 01:00:00") { // 10:00 KST: UTC로는 날짜가 바뀐 뒤여도 같은 서비스 하루
		t.Error("10:00 KST is still the same service day")
	}
	if !sentAt(sub, "2026-09-20 14:59:59") {
		t.Error("the last second of the interval must count")
	}
	if sentAt(sub, "2026-09-20 15:00:00") {
		t.Error("the end of the interval is the next day and must not count")
	}
	if sentAt(other, "2026-09-20 01:00:00") {
		t.Error("another subscriber's delivery must not count")
	}
}

func insertSubscriber2(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO subscribers(email, confirmed_at) VALUES ('b@example.com', CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}
