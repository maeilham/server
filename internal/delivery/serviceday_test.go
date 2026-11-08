package delivery

import (
	"context"
	"testing"
	"time"
	_ "time/tzdata"
)

// 서비스의 하루를 나누는 시간대. 운영 기본값(Asia/Seoul)과 같은 +09:00이다.
var testLoc = time.FixedZone("KST", 9*3600)

// 07:00 KST(= 전날 22:00 UTC)에 알린 뒤 같은 한국 날짜의 10:00 KST(= 01:00 UTC)에 다시 돌려도
// UTC 날짜가 바뀌었다고 2통째가 나가면 안 된다. 서비스의 하루는 한국 시간 기준이다.
func TestNotifyNewQuestions_RerunAcrossUTCMidnightSameServiceDay(t *testing.T) {
	db := newTestDB(t)
	seedOneSubscriber(t, db, "0001", "0002")
	m := &mockMailer{}
	morning := time.Date(2026, 5, 26, 22, 0, 0, 0, time.UTC) // 2026-05-27 07:00 KST
	later := time.Date(2026, 5, 27, 1, 0, 0, 0, time.UTC)    // 2026-05-27 10:00 KST

	if _, err := NotifyNewQuestions(context.Background(), discardLogger(), m, notifyOpts(db, morning)); err != nil {
		t.Fatal(err)
	}
	stats2, err := NotifyNewQuestions(context.Background(), discardLogger(), m, notifyOpts(db, later))
	if err != nil {
		t.Fatal(err)
	}

	if stats2.Sent != 0 || len(m.messages) != 1 {
		t.Errorf("rerun in the same KST day: Sent=%d mails=%d, want 0 and 1", stats2.Sent, len(m.messages))
	}
	var questions int
	_ = db.QueryRow(`SELECT COUNT(*) FROM daily_question`).Scan(&questions)
	if questions != 1 {
		t.Errorf("daily_question rows = %d, want 1 (같은 서비스 하루는 질문이 하나)", questions)
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

func TestNotifyNewQuestions_RequiresLoc(t *testing.T) {
	db := newTestDB(t)
	opts := notifyOpts(db, time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC))
	opts.Loc = nil
	if _, err := NotifyNewQuestions(context.Background(), discardLogger(), &mockMailer{}, opts); err == nil {
		t.Fatal("err = nil, want an error when Loc is missing")
	}
}

// 한국 자정을 지나면 UTC로는 아직 같은 날이어도 새 하루다: 23:59 KST와 00:00 KST 다음 날.
func TestNotifyNewQuestions_NewServiceDayStartsAtServiceMidnight(t *testing.T) {
	db := newTestDB(t)
	seedOneSubscriber(t, db, "0001", "0002")
	m := &mockMailer{}
	lastSecond := time.Date(2026, 5, 27, 14, 59, 59, 0, time.UTC) // 23:59:59 KST, 5/27
	midnight := time.Date(2026, 5, 27, 15, 0, 0, 0, time.UTC)     // 00:00:00 KST, 5/28

	if _, err := NotifyNewQuestions(context.Background(), discardLogger(), m, notifyOpts(db, lastSecond)); err != nil {
		t.Fatal(err)
	}
	stats2, err := NotifyNewQuestions(context.Background(), discardLogger(), m, notifyOpts(db, midnight))
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Sent != 1 || stats2.Skipped != 0 {
		t.Errorf("after service midnight: Sent=%d Skipped=%d, want 1 and 0", stats2.Sent, stats2.Skipped)
	}
	var sent string
	_ = db.QueryRow(`SELECT group_concat(content_id) FROM (SELECT content_id FROM delivery_log ORDER BY id)`).Scan(&sent)
	if sent != "0001,0002" {
		t.Errorf("notified contents = %q, want 0001 then 0002 (각 서비스 하루가 자기 질문을 하나씩 알림)", sent)
	}
}

func TestServiceDate(t *testing.T) {
	instant := time.Date(2026, 5, 27, 0, 30, 0, 0, kst) // 한국 5/27 00:30 = UTC 5/26 15:30
	if got := ServiceDate(instant, kst); got != "2026-05-27" {
		t.Errorf("KST = %s, want 2026-05-27", got)
	}
	if got := ServiceDate(instant.UTC(), kst); got != "2026-05-27" {
		t.Errorf("같은 순간을 UTC 표기로 넘겨도 한국 날짜여야 함: got %s", got)
	}
	if got := ServiceDate(instant, time.UTC); got != "2026-05-26" {
		t.Errorf("UTC 기준이면 전날: got %s", got)
	}
}
