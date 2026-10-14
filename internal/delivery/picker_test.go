package delivery

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/maeilham/server/internal/store"
)

// newToday는 실제 SQLite 저장소와 고정된 시각으로 Picker를 만든다.
func newToday(db *sql.DB, now time.Time) *Picker {
	return &Picker{
		Repos:     store.NewRepoStore(db),
		Subs:      store.NewSubscriberStore(db),
		Contents:  store.NewContentStore(db),
		Questions: store.NewDailyQuestionStore(db),
		Loc:       kst,
		Now:       func() time.Time { return now },
	}
}

// deliver는 delivery_log에 UTC 문자열 그대로 한 행을 넣는다 (Record가 저장하는 형식).
func deliver(t *testing.T, db *sql.DB, subID int64, repo, contentID, sentAtUTC string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO delivery_log(subscriber_id, repo_slug, content_id, sent_at) VALUES (?, ?, ?, ?)`,
		subID, repo, contentID, sentAtUTC)
}

func idOf(c *Question) string {
	if c == nil {
		return "<nil>"
	}
	return c.RepoSlug + "/" + c.ContentID
}

func mustToday(t *testing.T, s *Picker, repo string) *Question {
	t.Helper()
	c, err := s.contentOfDay(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ── repo의 오늘 글 ───────────────────────────────────────────────────────────

// 첫날은 아직 안 나온 새 글이고 복습이 아님.
func TestContentOfDay_FirstDayIsTheFirstNewContent(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")

	q := mustToday(t, newToday(db, time.Date(2026, 9, 20, 6, 0, 0, 0, kst)), "be")
	if idOf(q) != "be/0001" || q.Review {
		t.Errorf("got %s review=%v, want be/0001 review=false", idOf(q), q.Review)
	}
}

// 오늘의 질문은 발송과 무관함: 발송 기록이 생기거나 로테이션 값이 바뀌어도 하루 종일 같음.
func TestContentOfDay_IsIndependentOfDeliveries(t *testing.T) {
	db := newTestDB(t)
	sub := insertConfirmedSubscriber(t, db, "a@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")

	if got := idOf(mustToday(t, newToday(db, time.Date(2026, 9, 20, 6, 0, 0, 0, kst)), "be")); got != "be/0001" {
		t.Fatalf("setup: got %s, want be/0001", got)
	}
	// 그 뒤에 다른 글이 발송되고 로테이션이 바뀐 것처럼 만들어도
	deliver(t, db, sub, "be", "0002", "2026-09-19 22:00:00")
	mustExec(t, db, `UPDATE contents SET rotation_count = 5 WHERE content_id = '0001'`)

	if got := idOf(mustToday(t, newToday(db, time.Date(2026, 9, 20, 15, 0, 0, 0, kst)), "be")); got != "be/0001" {
		t.Errorf("got %s, want be/0001 (저장된 오늘의 질문이 유지되어야 함)", got)
	}
}

// 다음 날에는 다음 새 글이고, 새 글이 없으면 가장 오래전에 나온 글을 복습으로 보여 줌.
func TestContentOfDay_NextDaysThenReview(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")

	want := []struct {
		id     string
		review bool
	}{
		{"be/0001", false}, {"be/0002", false}, {"be/0001", true}, {"be/0002", true},
	}
	for i, w := range want {
		q := mustToday(t, newToday(db, time.Date(2026, 9, 20+i, 9, 0, 0, 0, kst)), "be")
		if idOf(q) != w.id || q.Review != w.review {
			t.Errorf("9/%d: got %s review=%v, want %s review=%v", 20+i, idOf(q), q.Review, w.id, w.review)
		}
	}
}

// 한국 시간 자정이 하루의 경계다. UTC 날짜로 나누면 한국 아침이 전날로 잡힌다.
func TestContentOfDay_KSTMidnightBoundary(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")

	lastSecond := time.Date(2026, 9, 20, 23, 59, 59, 0, kst) // UTC로는 9/20 14:59:59
	midnight := time.Date(2026, 9, 21, 0, 0, 0, 0, kst)      // UTC로는 9/20 15:00:00
	if got := idOf(mustToday(t, newToday(db, lastSecond), "be")); got != "be/0001" {
		t.Errorf("KST 자정 1초 전: got %s, want be/0001", got)
	}
	if got := idOf(mustToday(t, newToday(db, midnight), "be")); got != "be/0002" {
		t.Errorf("KST 자정 정각은 새 하루: got %s, want be/0002", got)
	}
	if got := idOf(mustToday(t, newToday(db, lastSecond), "be")); got != "be/0001" {
		t.Errorf("다시 물어도 그날 글은 그대로: got %s, want be/0001", got)
	}
}

// 오늘의 질문이던 글이 삭제되면 같은 날 다시 정함.
func TestContentOfDay_RepicksWhenTodaysContentIsDeleted(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	s := newToday(db, time.Date(2026, 9, 20, 12, 0, 0, 0, kst))

	if got := idOf(mustToday(t, s, "be")); got != "be/0001" {
		t.Fatalf("setup: got %s, want be/0001", got)
	}
	mustExec(t, db, `UPDATE contents SET deleted_at = CURRENT_TIMESTAMP WHERE content_id = '0001'`)

	if got := idOf(mustToday(t, s, "be")); got != "be/0002" {
		t.Errorf("got %s, want be/0002 (삭제된 글을 다시 정함)", got)
	}
}

func TestContentOfDay_RepoWithoutContent(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	s := newToday(db, time.Date(2026, 9, 20, 12, 0, 0, 0, kst))
	if c := mustToday(t, s, "be"); c != nil {
		t.Errorf("글이 없으면 nil이어야 함: %s", idOf(c))
	}
}

func TestContentOfDay_ReturnsFullRow(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	c := mustToday(t, newToday(db, time.Date(2026, 9, 20, 6, 0, 0, 0, kst)), "be")
	if c.RepoName != "be" || c.Title != "title-0001" || c.GitHubURL == "" {
		t.Errorf("전체 행이 아님(RepoName/Title/GitHubURL 누락): %+v", c)
	}
}

// ── 게스트용 오늘의 질문 ─────────────────────────────────────────────────────

func TestForGuest_SingleRepo(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	got, err := newToday(db, time.Date(2026, 9, 20, 6, 0, 0, 0, kst)).ForGuest(context.Background())
	if err != nil || idOf(got) != "be/0001" {
		t.Errorf("got %s err=%v, want be/0001", idOf(got), err)
	}
}

func TestForGuest_NothingToShow(t *testing.T) {
	db := newTestDB(t)
	got, err := newToday(db, time.Date(2026, 9, 20, 6, 0, 0, 0, kst)).ForGuest(context.Background())
	if err != nil || got != nil {
		t.Errorf("repo가 없으면 (nil, nil): got %s err=%v", idOf(got), err)
	}
	insertRepo(t, db, "be") // repo는 있지만 글이 없다
	got, err = newToday(db, time.Date(2026, 9, 20, 6, 0, 0, 0, kst)).ForGuest(context.Background())
	if err != nil || got != nil {
		t.Errorf("글이 없으면 (nil, nil): got %s err=%v", idOf(got), err)
	}
}

func TestForGuest_SkipsReposWithoutContent(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "aa") // 글 없음
	insertRepo(t, db, "zz")
	insertContent(t, db, "zz", "0001")
	for day := 1; day <= 30; day++ {
		s := newToday(db, time.Date(2026, 9, day, 12, 0, 0, 0, kst))
		if got, _ := s.ForGuest(context.Background()); idOf(got) != "zz/0001" {
			t.Fatalf("9/%d: got %s, want zz/0001 (글이 없는 repo가 뽑히면 안 됨)", day, idOf(got))
		}
	}
}

// 같은 날은 누가 언제 물어도 같은 글이고, 날이 바뀌면 repo가 고르게 섞여 나온다.
func TestForGuest_DeterministicPerDayAndBalancedAcrossDays(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertRepo(t, db, "fe")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "fe", "0001")

	counts := map[string]int{}
	for i := 0; i < 200; i++ {
		day := time.Date(2026, 1, 1, 0, 0, 0, 0, kst).AddDate(0, 0, i)
		morning := newToday(db, day.Add(1*time.Hour))
		evening := newToday(db, day.Add(22*time.Hour))
		a, _ := morning.ForGuest(context.Background())
		b, _ := evening.ForGuest(context.Background())
		if idOf(a) != idOf(b) {
			t.Fatalf("%s: 같은 날 아침(%s)과 저녁(%s)이 다름", day.Format("2006-01-02"), idOf(a), idOf(b))
		}
		counts[a.RepoSlug]++
	}
	if counts["be"] < 60 || counts["fe"] < 60 {
		t.Errorf("200일 동안의 분포 = %v, 두 repo가 고르게 나와야 함", counts)
	}
}

// 하루의 기준은 서비스 시간대(KST)다. 같은 순간을 UTC로 표현해도 결과가 같아야 한다.
func TestForGuest_UsesServiceTimeZoneForTheDay(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertRepo(t, db, "fe")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "fe", "0001")

	for i := 0; i < 60; i++ {
		instant := time.Date(2026, 3, 1, 0, 30, 0, 0, kst).AddDate(0, 0, i) // 매일 KST 00:30 = UTC 전날 15:30
		inKST, _ := newToday(db, instant.In(kst)).ForGuest(context.Background())
		inUTC, _ := newToday(db, instant.UTC()).ForGuest(context.Background())
		sameDayNoon, _ := newToday(db, time.Date(instant.In(kst).Year(), instant.In(kst).Month(), instant.In(kst).Day(), 12, 0, 0, 0, kst)).ForGuest(context.Background())
		if idOf(inKST) != idOf(inUTC) || idOf(inKST) != idOf(sameDayNoon) {
			t.Fatalf("%s: KST=%s UTC표현=%s 같은날 정오=%s", instant.In(kst).Format("2006-01-02 15:04"), idOf(inKST), idOf(inUTC), idOf(sameDayNoon))
		}
	}
}

// 게스트도 복습 여부를 함께 받음: 새 글이 있으면 false, 모두 나왔으면 true.
func TestForGuest_ReturnsTheReviewFlag(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")

	want := []bool{false, false, true}
	for i, w := range want {
		got, err := newToday(db, time.Date(2026, 9, 20+i, 9, 0, 0, 0, kst)).ForGuest(context.Background())
		if err != nil || got == nil || got.Review != w {
			t.Errorf("9/%d: got %v err=%v, want review=%v", 20+i, got, err, w)
		}
	}
}

// ── 가중 추첨 ────────────────────────────────────────────────────────────────

func TestPickByWeight(t *testing.T) {
	cases := []struct {
		weights []int
		seed    uint64
		want    int
	}{
		{[]int{1}, 12345, 0},
		{[]int{3, 1}, 0, 0}, {[]int{3, 1}, 2, 0}, {[]int{3, 1}, 3, 1}, {[]int{3, 1}, 4, 0}, // seed % 4
		{[]int{1, 1}, 0, 0}, {[]int{1, 1}, 1, 1}, {[]int{1, 1}, 1<<63 + 1, 1},
		{[]int{2, 3, 5}, 1, 0}, {[]int{2, 3, 5}, 2, 1}, {[]int{2, 3, 5}, 4, 1}, {[]int{2, 3, 5}, 5, 2}, {[]int{2, 3, 5}, 9, 2},
	}
	for _, tc := range cases {
		if got := pickByWeight(tc.weights, tc.seed); got != tc.want {
			t.Errorf("pickByWeight(%v, %d) = %d, want %d", tc.weights, tc.seed, got, tc.want)
		}
	}
}

// ── 메일과 API가 같은 Picker를 쓴다 ──────────────────────────────────────────

// 같은 repo의 같은 날은 누구에게나 같은 글이다. 게스트든 구독자든 뽑힌 repo의 글은 그 repo의 오늘 글이어야 한다.
func TestPicker_ChosenContentIsTheRepoContentOfDay(t *testing.T) {
	db := newTestDB(t)
	sub := insertConfirmedSubscriber(t, db, "a@example.com")
	insertRepo(t, db, "be")
	insertRepo(t, db, "fe")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	insertContent(t, db, "fe", "0001")
	subscribe(t, db, sub, "be", 3)
	subscribe(t, db, sub, "fe", 3)

	for i := 0; i < 60; i++ {
		p := pickerAt(db, time.Date(2026, 6, 1, 12, 0, 0, 0, kst).AddDate(0, 0, i))
		for name, pick := range map[string]func() (*Question, error){
			"guest":      func() (*Question, error) { return p.ForGuest(context.Background()) },
			"subscriber": func() (*Question, error) { return p.ForSubscriber(context.Background(), sub) },
		} {
			got, err := pick()
			if err != nil || got == nil {
				t.Fatalf("day %d %s: got %v err=%v", i, name, got, err)
			}
			want, _ := p.contentOfDay(context.Background(), got.RepoSlug)
			if idOf(got) != idOf(want) {
				t.Fatalf("day %d %s: picked %s but that repo's content of the day is %s", i, name, idOf(got), idOf(want))
			}
		}
	}
}

// 메일 배치가 보내는 글은 같은 구독자에게 API(같은 Picker)가 돌려주는 글과 같아야 한다.
func TestDailySend_SendsWhatThePickerReturnsForThatSubscriber(t *testing.T) {
	db := newTestDB(t)
	sub := insertConfirmedSubscriber(t, db, "a@example.com")
	insertRepo(t, db, "be")
	insertRepo(t, db, "fe")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "fe", "0001")
	subscribe(t, db, sub, "be", 3)
	subscribe(t, db, sub, "fe", 3)

	for i := 0; i < 40; i++ {
		sendAt := time.Date(2026, 6, 1, 7, 0, 0, 0, testLoc).AddDate(0, 0, i) // 매일 07:00 KST
		want, err := pickerAt(db, sendAt).ForSubscriber(context.Background(), sub)
		if err != nil || want == nil {
			t.Fatalf("day %d: picker got %v err=%v", i, want, err)
		}
		if _, err := DailySend(context.Background(), discardLogger(), &mockMailer{}, DailySendOptions{
			Day: sendAt, Loc: testLoc,
			SubRepo: store.NewSubscriberStore(db), RepoStore: store.NewRepoStore(db),
			ContentStore: store.NewContentStore(db), QuestionStore: store.NewDailyQuestionStore(db), LogStore: store.NewDeliveryLogStore(db),
		}); err != nil {
			t.Fatal(err)
		}
		var repo, content string
		if err := db.QueryRow(`SELECT repo_slug, content_id FROM delivery_log ORDER BY id DESC LIMIT 1`).Scan(&repo, &content); err != nil {
			t.Fatal(err)
		}
		if repo+"/"+content != idOf(want) {
			t.Fatalf("day %d: mail sent %s/%s, picker returns %s", i, repo, content, idOf(want))
		}
	}
}

// 이슈 #2에 남아 있던 한계: 발송이 끝나 로테이션이 넘어간 뒤 같은 날 재실행하면, 뒤늦게 가입한 구독자는
// 이미 넘어간 "다음 글"을 받았다. 같은 repo의 같은 날은 모두에게 같은 글이어야 한다.
func TestDailySend_LateSubscriberGetsTheSameContentAsEveryoneToday(t *testing.T) {
	db := newTestDB(t)
	early := insertConfirmedSubscriber(t, db, "early@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	subscribe(t, db, early, "be", 3)

	run := func(at time.Time) {
		t.Helper()
		if _, err := DailySend(context.Background(), discardLogger(), &mockMailer{}, DailySendOptions{
			Day: at, Loc: testLoc,
			SubRepo: store.NewSubscriberStore(db), RepoStore: store.NewRepoStore(db),
			ContentStore: store.NewContentStore(db), QuestionStore: store.NewDailyQuestionStore(db), LogStore: store.NewDeliveryLogStore(db),
		}); err != nil {
			t.Fatal(err)
		}
	}
	run(time.Date(2026, 5, 27, 7, 0, 0, 0, testLoc)) // 정기 발송: 0001이 나가고 로테이션이 0002를 가리킨다

	late := insertConfirmedSubscriber(t, db, "late@example.com") // 발송 뒤에 가입
	subscribe(t, db, late, "be", 3)
	run(time.Date(2026, 5, 27, 10, 0, 0, 0, testLoc)) // 같은 날 재실행

	contentOf := func(id int64) string {
		var c string
		if err := db.QueryRow(`SELECT content_id FROM delivery_log WHERE subscriber_id = ?`, id).Scan(&c); err != nil {
			t.Fatalf("subscriber %d has no delivery: %v", id, err)
		}
		return c
	}
	if e, l := contentOf(early), contentOf(late); e != "0001" || l != "0001" {
		t.Errorf("early got %s, late got %s, want both 0001 (the content of the day)", e, l)
	}
}

// 게스트는 "활성 repo를 모두 같은 가중치로 구독한 id 0의 가상 구독자"다. 이 약속이 바뀌면 같은 날 게스트가 보는 repo가
// 바뀌므로 고정해 둔다(실제 구독자 id는 1부터라 겹치지 않는다).
func TestForGuest_IsTheVirtualSubscriberZero(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertRepo(t, db, "fe")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "fe", "0001")

	slugs := []string{"be", "fe"} // ListActive는 slug 순
	for i := 0; i < 60; i++ {
		now := time.Date(2026, 6, 1, 12, 0, 0, 0, kst).AddDate(0, 0, i)
		want := slugs[pickByWeight([]int{1, 1}, dailyPickSeed(0, now, kst))]
		got, err := pickerAt(db, now).ForGuest(context.Background())
		if err != nil || got == nil || got.RepoSlug != want {
			t.Fatalf("day %d: got %s err=%v, want repo %s (id 0 seed, equal weights)", i, idOf(got), err, want)
		}
	}
}

// ---------------- TodayContentForRepo ----------------

func TestTodayForRepo_Empty(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	_, cs := newStores(db)

	got, err := cs.TodayForRepo(context.Background(), "be")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty repo, got %+v", got)
	}
}

func TestTodayForRepo_OrdersByRotationThenContentID(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	insertContent(t, db, "be", "0003")

	// 0001 already rotated, so 0002 should now be "today"
	mustExec(t, db, `UPDATE contents SET rotation_count = 1 WHERE content_id = '0001'`)
	_, cs := newStores(db)

	got, err := cs.TodayForRepo(context.Background(), "be")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got == nil || got.ContentID != "0002" {
		t.Errorf("expected 0002, got %+v", got)
	}
}

func TestTodayForRepo_IgnoresDeleted(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	mustExec(t, db, `UPDATE contents SET deleted_at = CURRENT_TIMESTAMP WHERE content_id = '0001'`)
	_, cs := newStores(db)

	got, err := cs.TodayForRepo(context.Background(), "be")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got == nil || got.ContentID != "0002" {
		t.Errorf("expected 0002 (0001 is deleted), got %+v", got)
	}
}

// ---------------- ForSubscriber ----------------

func TestForSubscriber_Unconfirmed(t *testing.T) {
	db := newTestDB(t)
	res, _ := db.Exec(`INSERT INTO subscribers(email) VALUES (?)`, "x@x.kr")
	id, _ := res.LastInsertId()
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	subscribe(t, db, id, "be", 3)

	got, err := pickerAt(db, time.Now()).ForSubscriber(context.Background(), id)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("unconfirmed subscriber should not receive: %+v", got)
	}
}

func TestForSubscriber_Paused(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "x@x.kr")
	mustExec(t, db, `UPDATE subscribers SET paused_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	subscribe(t, db, id, "be", 3)

	got, err := pickerAt(db, time.Now()).ForSubscriber(context.Background(), id)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("paused subscriber should not receive: %+v", got)
	}
}

func TestForSubscriber_NoSubscriptions(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "x@x.kr")

	got, err := pickerAt(db, time.Now()).ForSubscriber(context.Background(), id)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("no-subscription subscriber should not receive: %+v", got)
	}
}

func TestForSubscriber_NoEligibleContent(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "x@x.kr")
	insertRepo(t, db, "be") // no contents
	subscribe(t, db, id, "be", 3)

	got, err := pickerAt(db, time.Now()).ForSubscriber(context.Background(), id)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when no contents exist, got %+v", got)
	}
}

func TestForSubscriber_SingleSubscription(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "x@x.kr")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	subscribe(t, db, id, "be", 3)

	got, err := pickerAt(db, time.Now()).ForSubscriber(context.Background(), id)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got == nil || got.RepoSlug != "be" || got.ContentID != "0001" {
		t.Errorf("expected be/0001, got %+v", got)
	}
}

func TestForSubscriber_IgnoresInactiveRepo(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "x@x.kr")
	insertRepo(t, db, "be")
	insertRepo(t, db, "fe")
	mustExec(t, db, `UPDATE repos SET active = 0 WHERE slug = 'be'`)
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "fe", "0001")
	subscribe(t, db, id, "be", 5)
	subscribe(t, db, id, "fe", 1)

	got, err := pickerAt(db, time.Now()).ForSubscriber(context.Background(), id)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got == nil || got.RepoSlug != "fe" {
		t.Errorf("inactive repo should be skipped; got %+v", got)
	}
}

func TestForSubscriber_DeterministicSameDay(t *testing.T) {
	db := newTestDB(t)
	id := insertConfirmedSubscriber(t, db, "x@x.kr")
	insertRepo(t, db, "be")
	insertRepo(t, db, "fe")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "fe", "0001")
	subscribe(t, db, id, "be", 3)
	subscribe(t, db, id, "fe", 3)

	day := time.Date(2026, 5, 24, 0, 0, 0, 0, time.UTC)
	first, _ := pickerAt(db, day).ForSubscriber(context.Background(), id)
	for range 5 {
		got, _ := pickerAt(db, day).ForSubscriber(context.Background(), id)
		if got.RepoSlug != first.RepoSlug || got.ContentID != first.ContentID {
			t.Fatalf("not deterministic: first=%v, retry=%v", first, got)
		}
	}
}

func TestForSubscriber_DifferentDayMayDiffer(t *testing.T) {
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
	first, _ := pickerAt(db, day0).ForSubscriber(context.Background(), id)
	differs := false
	for i := 1; i <= 60; i++ {
		got, _ := pickerAt(db, day0.AddDate(0, 0, i)).ForSubscriber(context.Background(), id)
		if got.RepoSlug != first.RepoSlug {
			differs = true
			break
		}
	}
	if !differs {
		t.Errorf("expected pick to differ across days; same repo every day for 60 days")
	}
}

func TestForSubscriber_WeightDistribution(t *testing.T) {
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
		subscribe(t, db, id, "be", 5)
		subscribe(t, db, id, "fe", 1)
		got, _ := pickerAt(db, time.Now()).ForSubscriber(context.Background(), id)
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

// 시드는 서비스 시간대의 달력 날짜에만 달려 있다. 같은 순간을 어느 시간대 표기로 넘겨도 같아야 하고,
// UTC로는 날짜가 다른 두 시각도 한국 하루가 같으면 같아야 하며, 한국 날짜가 바뀌면 달라야 한다.
func TestDailyPickSeed_DependsOnServiceDateOnly(t *testing.T) {
	const id = 7
	instant := time.Date(2026, 5, 27, 0, 30, 0, 0, kst) // 한국 5/27 00:30 = UTC 5/26 15:30

	if a, b := dailyPickSeed(id, instant, kst), dailyPickSeed(id, instant.UTC(), kst); a != b {
		t.Errorf("같은 순간인데 표기(KST/UTC)에 따라 시드가 다름: %d vs %d", a, b)
	}
	lateSameDay := time.Date(2026, 5, 27, 23, 30, 0, 0, kst) // 한국 5/27 23:30 = UTC 5/27 14:30 (UTC 날짜가 다름)
	if a, b := dailyPickSeed(id, instant, kst), dailyPickSeed(id, lateSameDay, kst); a != b {
		t.Errorf("한국 같은 날(UTC 날짜는 다름)인데 시드가 다름: %d vs %d", a, b)
	}
	prevDay := time.Date(2026, 5, 26, 23, 30, 0, 0, kst) // 한국 5/26 = 전날
	if a, b := dailyPickSeed(id, instant, kst), dailyPickSeed(id, prevDay, kst); a == b {
		t.Errorf("한국 날짜가 다른데 시드가 같음: %d", a)
	}
	if a, b := dailyPickSeed(1, instant, kst), dailyPickSeed(2, instant, kst); a == b {
		t.Errorf("구독자가 다른데 시드가 같음: %d", a)
	}
}

func TestDailySend_RequiresQuestionStore(t *testing.T) {
	db := newTestDB(t)
	_, err := DailySend(context.Background(), discardLogger(), &mockMailer{}, DailySendOptions{
		Day: time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC), Loc: kst,
		SubRepo: store.NewSubscriberStore(db), RepoStore: store.NewRepoStore(db),
		ContentStore: store.NewContentStore(db), LogStore: store.NewDeliveryLogStore(db),
	})
	if err == nil {
		t.Fatal("err = nil, want an error when QuestionStore is missing")
	}
}

// 구독자도 복습 여부를 함께 받음(메일과 API가 같은 Picker를 쓰므로 같은 값).

// 구독자도 복습 여부를 함께 받음(메일과 API가 같은 Picker를 쓰므로 같은 값).
func TestForSubscriber_ReturnsTheReviewFlag(t *testing.T) {
	db := newTestDB(t)
	sub := insertConfirmedSubscriber(t, db, "a@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	subscribe(t, db, sub, "be", 3)

	for i, want := range []bool{false, true, true} { // 글이 하나라 첫날 이후는 모두 복습
		got, err := pickerAt(db, time.Date(2026, 9, 20+i, 9, 0, 0, 0, kst)).ForSubscriber(context.Background(), sub)
		if err != nil || got == nil || got.Review != want {
			t.Errorf("day %d: got %v err=%v, want review=%v", i, got, err, want)
		}
	}
}

// 메일 배치가 만든 오늘의 질문 행은 API가 읽는 행과 같음(발송이 질문을 정함).

// 메일 배치가 만든 오늘의 질문 행은 API가 읽는 행과 같음(발송이 질문을 정함).
func TestDailySend_StoresTheDayQuestionThatTheAPIReads(t *testing.T) {
	db := newTestDB(t)
	sub := insertConfirmedSubscriber(t, db, "a@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	subscribe(t, db, sub, "be", 3)
	sendAt := time.Date(2026, 9, 20, 7, 0, 0, 0, kst)

	if _, err := DailySend(context.Background(), discardLogger(), &mockMailer{}, DailySendOptions{
		Day: sendAt, Loc: kst,
		SubRepo: store.NewSubscriberStore(db), RepoStore: store.NewRepoStore(db),
		ContentStore: store.NewContentStore(db), LogStore: store.NewDeliveryLogStore(db),
		QuestionStore: store.NewDailyQuestionStore(db),
	}); err != nil {
		t.Fatal(err)
	}
	var content string
	if err := db.QueryRow(`SELECT content_id FROM daily_question WHERE day = '2026-09-20' AND repo_slug = 'be'`).Scan(&content); err != nil {
		t.Fatalf("no stored question for the sent day: %v", err)
	}
	got, _ := newToday(db, time.Date(2026, 9, 20, 15, 0, 0, 0, kst)).ForGuest(context.Background())
	if content != "0001" || idOf(got) != "be/0001" {
		t.Errorf("stored=%s api=%s, want both 0001", content, idOf(got))
	}
}
