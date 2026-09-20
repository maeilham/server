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

func TestLastDeliveredContentID(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertRepo(t, db, "fe", "프론트엔드", 1)
	for _, c := range [][2]string{{"be", "0001"}, {"be", "0002"}, {"be", "0003"}, {"fe", "0001"}} {
		insertContent(t, db, c[0], c[1], c[0]+"/"+c[1])
	}
	sub := insertSubscriber(t, db)
	// 한국 시간 2026-09-20 하루 = UTC 2026-09-19 15:00:00 이상, 2026-09-20 15:00:00 미만
	insertDelivery(t, db, sub, "be", "0001", "2026-09-19 14:59:59") // 구간 직전: 제외
	insertDelivery(t, db, sub, "be", "0002", "2026-09-19 22:00:00") // KST 07:00: 포함
	insertDelivery(t, db, sub, "be", "0003", "2026-09-20 14:59:59") // 구간 마지막 순간: 포함, 가장 최근
	insertDelivery(t, db, sub, "be", "0001", "2026-09-20 15:00:00") // 구간 끝(미포함): 제외
	insertDelivery(t, db, sub, "fe", "0001", "2026-09-19 22:00:00") // 다른 repo
	ls := store.NewDeliveryLogStore(db)
	ctx := context.Background()

	kst := time.FixedZone("KST", 9*3600)
	from := time.Date(2026, 9, 20, 0, 0, 0, 0, kst) // KST로 줘도 UTC로 바꿔 비교해야 한다
	to := from.AddDate(0, 0, 1)

	id, found, err := ls.LastDeliveredContentID(ctx, "be", from, to)
	if err != nil || !found || id != "0003" {
		t.Errorf("be: id=%q found=%v err=%v, want 0003 (구간 안에서 가장 최근)", id, found, err)
	}
	if id, found, _ := ls.LastDeliveredContentID(ctx, "fe", from, to); !found || id != "0001" {
		t.Errorf("fe: id=%q found=%v (repo가 섞이면 안 됨)", id, found)
	}

	// 경계: 시작은 포함(>=), 끝은 제외(<)
	utc := func(s string) time.Time {
		tm, _ := time.Parse("2006-01-02 15:04:05", s)
		return tm
	}
	if id, found, _ := ls.LastDeliveredContentID(ctx, "be", utc("2026-09-19 22:00:00"), utc("2026-09-19 22:00:01")); !found || id != "0002" {
		t.Errorf("시작 시각과 같은 기록은 포함돼야 함: id=%q found=%v", id, found)
	}
	if _, found, _ := ls.LastDeliveredContentID(ctx, "be", utc("2026-09-19 21:00:00"), utc("2026-09-19 22:00:00")); found {
		t.Error("끝 시각과 같은 기록은 제외돼야 함")
	}

	// 기록이 없는 구간/repo
	if _, found, err := ls.LastDeliveredContentID(ctx, "be", utc("2026-01-01 00:00:00"), utc("2026-01-02 00:00:00")); found || err != nil {
		t.Errorf("빈 구간: found=%v err=%v", found, err)
	}
	if _, found, _ := ls.LastDeliveredContentID(ctx, "nope", from, to); found {
		t.Error("없는 repo는 찾지 못해야 함")
	}
}

// 같은 시각에 여러 글이 기록돼도 결과는 항상 같아야 한다 (content_id 내림차순).
func TestLastDeliveredContentID_TieBreak(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be", "백엔드", 1)
	insertContent(t, db, "be", "0001", "a")
	insertContent(t, db, "be", "0002", "b")
	sub := insertSubscriber(t, db)
	insertDelivery(t, db, sub, "be", "0001", "2026-09-19 22:00:00")
	insertDelivery(t, db, sub, "be", "0002", "2026-09-19 22:00:00")

	from := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	id, found, err := store.NewDeliveryLogStore(db).LastDeliveredContentID(context.Background(), "be", from, from.AddDate(0, 0, 2))
	if err != nil || !found || id != "0002" {
		t.Errorf("id=%q found=%v err=%v, want 0002", id, found, err)
	}
}
