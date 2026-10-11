package delivery

import (
	"context"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/maeilham/server/internal/store"
)

// 서비스의 하루를 나누는 시간대. 운영 기본값(Asia/Seoul)과 같은 +09:00이다.
var testLoc = time.FixedZone("KST", 9*3600)

// 이슈 #2: 07:00 KST(= 전날 22:00 UTC)에 정기 발송한 뒤 같은 한국 날짜의 10:00 KST(= 01:00 UTC)에
// 재실행하면 UTC 날짜가 바뀌어서 2통째가 나갔다. 서비스의 하루는 한국 시간 기준이므로 건너뛰어야 한다.
func TestDailySend_RerunAcrossUTCMidnightSameServiceDay(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "me@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	subscribe(t, db, id, "be", 3)

	mockM := &mockMailer{}
	mk := func(day time.Time) DailySendOptions {
		return DailySendOptions{
			Day:          day,
			Loc:          testLoc,
			SubRepo:      store.NewSubscriberStore(db),
			RepoStore:    store.NewRepoStore(db),
			ContentStore: store.NewContentStore(db),
			LogStore:     store.NewDeliveryLogStore(db),
		}
	}
	morning := time.Date(2026, 5, 26, 22, 0, 0, 0, time.UTC) // 2026-05-27 07:00 KST
	later := time.Date(2026, 5, 27, 1, 0, 0, 0, time.UTC)    // 2026-05-27 10:00 KST

	if _, err := DailySend(context.Background(), discardLogger(), mockM, mk(morning)); err != nil {
		t.Fatal(err)
	}
	stats2, err := DailySend(context.Background(), discardLogger(), mockM, mk(later))
	if err != nil {
		t.Fatal(err)
	}

	if stats2.Sent != 0 || stats2.Skipped != 1 {
		t.Errorf("rerun in the same KST day: Sent=%d Skipped=%d, want 0 and 1", stats2.Sent, stats2.Skipped)
	}
	if len(mockM.messages) != 1 {
		t.Errorf("sent %d mails, want 1", len(mockM.messages))
	}
	var rotation int
	_ = db.QueryRow(`SELECT rotation_count FROM contents WHERE content_id = '0001'`).Scan(&rotation)
	if rotation != 1 {
		t.Errorf("0001 rotation_count = %d, want 1 (advanced once)", rotation)
	}
}

func TestServiceDay(t *testing.T) {
	utc := func(y int, m time.Month, d, h, mi, s int) time.Time { return time.Date(y, m, d, h, mi, s, 0, time.UTC) }

	// 같은 한국 하루 안의 어느 시각이든 같은 구간이고, 구간은 UTC로 환산하면 전날 15:00부터다.
	for _, now := range []time.Time{
		utc(2026, 5, 26, 15, 0, 0), // 00:00:00 KST (시작은 포함)
		utc(2026, 5, 26, 22, 0, 0), // 07:00 KST
		utc(2026, 5, 27, 1, 0, 0),  // 10:00 KST (UTC로는 날짜가 바뀐 뒤)
		utc(2026, 5, 27, 14, 59, 59),
	} {
		start, end := ServiceDay(now, testLoc)
		if !start.Equal(utc(2026, 5, 26, 15, 0, 0)) || !end.Equal(utc(2026, 5, 27, 15, 0, 0)) {
			t.Errorf("ServiceDay(%s) = [%s, %s), want [05-26 15:00Z, 05-27 15:00Z)", now.Format(time.RFC3339), start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
		}
	}
	// 끝(다음 날 0시)은 다음 하루의 시작이다.
	if start, _ := ServiceDay(utc(2026, 5, 27, 15, 0, 0), testLoc); !start.Equal(utc(2026, 5, 27, 15, 0, 0)) {
		t.Errorf("00:00 KST of the next day must start a new day, got start %s", start.UTC().Format(time.RFC3339))
	}
	// 어느 시간대 표기로 넘겨도 같은 구간이다.
	a1, b1 := ServiceDay(utc(2026, 5, 27, 1, 0, 0), testLoc)
	a2, b2 := ServiceDay(utc(2026, 5, 27, 1, 0, 0).In(time.FixedZone("X", -5*3600)), testLoc)
	if !a1.Equal(a2) || !b1.Equal(b2) {
		t.Errorf("result depends on the zone t is expressed in: [%s,%s) vs [%s,%s)", a1, b1, a2, b2)
	}
}

// 서머타임이 있는 시간대에서는 하루가 23시간이거나 25시간이다. 24시간을 더하면 안 된다.
func TestServiceDay_DaylightSavingLength(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	spring, _ := ServiceDay(time.Date(2026, 3, 8, 12, 0, 0, 0, ny), ny) // 3/8 02:00에 시계가 앞으로 감: 23시간
	if s, e := ServiceDay(spring, ny); e.Sub(s) != 23*time.Hour {
		t.Errorf("spring-forward day length = %s, want 23h", e.Sub(s))
	}
	if s, e := ServiceDay(time.Date(2026, 11, 1, 12, 0, 0, 0, ny), ny); e.Sub(s) != 25*time.Hour {
		t.Errorf("fall-back day length = %s, want 25h", e.Sub(s))
	}
}

func TestDailySend_RequiresLoc(t *testing.T) {
	db := newTestDB(t)
	_, err := DailySend(context.Background(), discardLogger(), &mockMailer{}, DailySendOptions{
		Day:          time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC),
		SubRepo:      store.NewSubscriberStore(db),
		RepoStore:    store.NewRepoStore(db),
		ContentStore: store.NewContentStore(db),
		LogStore:     store.NewDeliveryLogStore(db),
	})
	if err == nil {
		t.Fatal("err = nil, want an error when Loc is missing")
	}
}

// 한국 자정을 지나면 UTC로는 아직 같은 날이어도 새 하루다: 23:59 KST와 00:00 KST 다음 날.
func TestDailySend_NewServiceDayStartsAtServiceMidnight(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "me@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	subscribe(t, db, id, "be", 3)

	mockM := &mockMailer{}
	mk := func(day time.Time) DailySendOptions {
		return DailySendOptions{
			Day: day, Loc: testLoc,
			SubRepo: store.NewSubscriberStore(db), RepoStore: store.NewRepoStore(db),
			ContentStore: store.NewContentStore(db), LogStore: store.NewDeliveryLogStore(db),
		}
	}
	lastSecond := time.Date(2026, 5, 27, 14, 59, 59, 0, time.UTC) // 23:59:59 KST, 5/27
	midnight := time.Date(2026, 5, 27, 15, 0, 0, 0, time.UTC)     // 00:00:00 KST, 5/28

	if _, err := DailySend(context.Background(), discardLogger(), mockM, mk(lastSecond)); err != nil {
		t.Fatal(err)
	}
	stats2, err := DailySend(context.Background(), discardLogger(), mockM, mk(midnight))
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Sent != 1 || stats2.Skipped != 0 {
		t.Errorf("after service midnight: Sent=%d Skipped=%d, want 1 and 0", stats2.Sent, stats2.Skipped)
	}
	var r1, r2 int
	_ = db.QueryRow(`SELECT rotation_count FROM contents WHERE content_id = '0001'`).Scan(&r1)
	_ = db.QueryRow(`SELECT rotation_count FROM contents WHERE content_id = '0002'`).Scan(&r2)
	if r1 != 1 || r2 != 1 {
		t.Errorf("rotation_count 0001=%d 0002=%d, want 1 and 1 (each day advanced its own content once)", r1, r2)
	}
}

// 추첨 시드의 날짜도 서비스 시간대 기준이어야 한다. 00:30 KST(= 전날 15:30 UTC)는 UTC 날짜와 한국 날짜가 다른 시각이다.
func TestDailySend_PickSeedUsesServiceDate(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "me@example.com")
	insertRepo(t, db, "a")
	insertRepo(t, db, "b")
	insertContent(t, db, "a", "0001")
	insertContent(t, db, "b", "0001")
	subscribe(t, db, id, "a", 3)
	subscribe(t, db, id, "b", 3)

	mockM := &mockMailer{}
	for i := 0; i < 60; i++ {
		kst := time.Date(2026, 6, 1+i, 0, 30, 0, 0, testLoc)
		day := kst.UTC() // 일부러 UTC 표기로 넘긴다
		opts := DailySendOptions{
			Day: day, Loc: testLoc,
			SubRepo: store.NewSubscriberStore(db), RepoStore: store.NewRepoStore(db),
			ContentStore: store.NewContentStore(db), LogStore: store.NewDeliveryLogStore(db),
		}
		if _, err := DailySend(context.Background(), discardLogger(), mockM, opts); err != nil {
			t.Fatal(err)
		}
		var got string
		if err := db.QueryRow(`SELECT repo_slug FROM delivery_log ORDER BY id DESC LIMIT 1`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		want := []string{"a", "b"}[pickByWeight([]int{3, 3}, dailyPickSeed(id, kst))]
		if got != want {
			t.Fatalf("day %s: picked repo %q, want %q (seed must use the service-zone date)", kst.Format("2006-01-02"), got, want)
		}
	}
}
