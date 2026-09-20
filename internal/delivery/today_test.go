package delivery

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/maeilham/server/internal/store"
)

var kst = time.FixedZone("KST", 9*3600)

// newToday는 실제 SQLite 저장소와 고정된 시각으로 TodayService를 만든다.
func newToday(db *sql.DB, now time.Time) *TodayService {
	return &TodayService{
		Repos:    store.NewRepoStore(db),
		Contents: store.NewContentStore(db),
		Log:      store.NewDeliveryLogStore(db),
		Loc:      kst,
		Now:      func() time.Time { return now },
	}
}

// deliver는 delivery_log에 UTC 문자열 그대로 한 행을 넣는다 (Record가 저장하는 형식).
func deliver(t *testing.T, db *sql.DB, subID int64, repo, contentID, sentAtUTC string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO delivery_log(subscriber_id, repo_slug, content_id, sent_at) VALUES (?, ?, ?, ?)`,
		subID, repo, contentID, sentAtUTC)
}

func idOf(c *store.Content) string {
	if c == nil {
		return "<nil>"
	}
	return c.RepoSlug + "/" + c.ContentID
}

func mustToday(t *testing.T, s *TodayService, repo string) *store.Content {
	t.Helper()
	c, err := s.ContentOfDay(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ── repo의 오늘 글 ───────────────────────────────────────────────────────────

func TestContentOfDay_BeforeSendIsNextInRotation(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")

	s := newToday(db, time.Date(2026, 9, 20, 6, 0, 0, 0, kst)) // 발송(07:00) 전
	if got := idOf(mustToday(t, s, "be")); got != "be/0001" {
		t.Errorf("got %s, want be/0001 (아직 한 번도 안 나갔으니 첫 글)", got)
	}
}

// 발송이 끝나면 로테이션이 넘어가 TodayForRepo는 내일 글을 가리킨다. 오늘의 글은 이미 나간 글이어야 한다.
func TestContentOfDay_AfterSendIsTheDeliveredContent(t *testing.T) {
	db := newTestDB(t)
	sub := insertConfirmedSubscriber(t, db, "a@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	// 오늘 KST 07:00(= UTC 전날 22:00)에 0001이 나갔고, 배치가 로테이션을 넘겼다
	deliver(t, db, sub, "be", "0001", "2026-09-19 22:00:00")
	mustExec(t, db, `UPDATE contents SET rotation_count = 1 WHERE content_id = '0001'`)

	s := newToday(db, time.Date(2026, 9, 20, 15, 0, 0, 0, kst))
	next, _ := store.NewContentStore(db).TodayForRepo(context.Background(), "be")
	if next.ContentID != "0002" {
		t.Fatalf("전제 확인: 발송 후 TodayForRepo는 다음 글(0002)이어야 함, got %s", next.ContentID)
	}
	if got := idOf(mustToday(t, s, "be")); got != "be/0001" {
		t.Errorf("got %s, want be/0001 (오늘 나간 글. 내일 글이 보이면 안 됨)", got)
	}
}

func TestContentOfDay_NextDayMovesOn(t *testing.T) {
	db := newTestDB(t)
	sub := insertConfirmedSubscriber(t, db, "a@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	deliver(t, db, sub, "be", "0001", "2026-09-19 22:00:00") // 9/20 07:00 KST
	mustExec(t, db, `UPDATE contents SET rotation_count = 1 WHERE content_id = '0001'`)

	// 다음 날 00:10 KST: 어제 발송은 오늘 구간 밖이고, 오늘 발송은 아직 전이다
	s := newToday(db, time.Date(2026, 9, 21, 0, 10, 0, 0, kst))
	if got := idOf(mustToday(t, s, "be")); got != "be/0002" {
		t.Errorf("got %s, want be/0002 (오늘 발송 전이니 로테이션의 다음 글)", got)
	}
}

// 한국 시간 자정이 하루의 경계다. UTC 날짜로 나누면 07:00 발송이 전날로 잡힌다.
func TestContentOfDay_KSTMidnightBoundary(t *testing.T) {
	db := newTestDB(t)
	sub := insertConfirmedSubscriber(t, db, "a@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	mustExec(t, db, `UPDATE contents SET rotation_count = 1 WHERE content_id = '0001'`)
	s := newToday(db, time.Date(2026, 9, 20, 12, 0, 0, 0, kst))

	// 9/19 23:59:59 KST (= UTC 14:59:59): 어제로 취급
	deliver(t, db, sub, "be", "0001", "2026-09-19 14:59:59")
	if got := idOf(mustToday(t, s, "be")); got != "be/0002" {
		t.Errorf("KST 자정 1초 전 발송은 어제 것: got %s, want be/0002", got)
	}
	// 9/20 00:00:00 KST (= UTC 15:00:00): 오늘로 취급
	mustExec(t, db, `DELETE FROM delivery_log`)
	deliver(t, db, sub, "be", "0001", "2026-09-19 15:00:00")
	if got := idOf(mustToday(t, s, "be")); got != "be/0001" {
		t.Errorf("KST 자정 정각 발송은 오늘 것: got %s, want be/0001", got)
	}
}

func TestContentOfDay_DeletedDeliveredContentFallsBack(t *testing.T) {
	db := newTestDB(t)
	sub := insertConfirmedSubscriber(t, db, "a@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	deliver(t, db, sub, "be", "0002", "2026-09-19 22:00:00")
	mustExec(t, db, `UPDATE contents SET deleted_at = CURRENT_TIMESTAMP WHERE content_id = '0002'`)

	s := newToday(db, time.Date(2026, 9, 20, 12, 0, 0, 0, kst))
	if got := idOf(mustToday(t, s, "be")); got != "be/0001" {
		t.Errorf("got %s, want be/0001 (오늘 나간 글이 삭제됐으면 로테이션의 다음 글)", got)
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

// ── 방문자용 오늘의 질문 ─────────────────────────────────────────────────────

func TestForVisitor_SingleRepo(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	got, err := newToday(db, time.Date(2026, 9, 20, 6, 0, 0, 0, kst)).ForVisitor(context.Background())
	if err != nil || idOf(got) != "be/0001" {
		t.Errorf("got %s err=%v, want be/0001", idOf(got), err)
	}
}

func TestForVisitor_NothingToShow(t *testing.T) {
	db := newTestDB(t)
	got, err := newToday(db, time.Date(2026, 9, 20, 6, 0, 0, 0, kst)).ForVisitor(context.Background())
	if err != nil || got != nil {
		t.Errorf("repo가 없으면 (nil, nil): got %s err=%v", idOf(got), err)
	}
	insertRepo(t, db, "be") // repo는 있지만 글이 없다
	got, err = newToday(db, time.Date(2026, 9, 20, 6, 0, 0, 0, kst)).ForVisitor(context.Background())
	if err != nil || got != nil {
		t.Errorf("글이 없으면 (nil, nil): got %s err=%v", idOf(got), err)
	}
}

func TestForVisitor_SkipsReposWithoutContent(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "aa") // 글 없음
	insertRepo(t, db, "zz")
	insertContent(t, db, "zz", "0001")
	for day := 1; day <= 30; day++ {
		s := newToday(db, time.Date(2026, 9, day, 12, 0, 0, 0, kst))
		if got, _ := s.ForVisitor(context.Background()); idOf(got) != "zz/0001" {
			t.Fatalf("9/%d: got %s, want zz/0001 (글이 없는 repo가 뽑히면 안 됨)", day, idOf(got))
		}
	}
}

// 같은 날은 누가 언제 물어도 같은 글이고, 날이 바뀌면 repo가 고르게 섞여 나온다.
func TestForVisitor_DeterministicPerDayAndBalancedAcrossDays(t *testing.T) {
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
		a, _ := morning.ForVisitor(context.Background())
		b, _ := evening.ForVisitor(context.Background())
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
func TestForVisitor_UsesServiceTimeZoneForTheDay(t *testing.T) {
	db := newTestDB(t)
	insertRepo(t, db, "be")
	insertRepo(t, db, "fe")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "fe", "0001")

	for i := 0; i < 60; i++ {
		instant := time.Date(2026, 3, 1, 0, 30, 0, 0, kst).AddDate(0, 0, i) // 매일 KST 00:30 = UTC 전날 15:30
		inKST, _ := newToday(db, instant.In(kst)).ForVisitor(context.Background())
		inUTC, _ := newToday(db, instant.UTC()).ForVisitor(context.Background())
		sameDayNoon, _ := newToday(db, time.Date(instant.In(kst).Year(), instant.In(kst).Month(), instant.In(kst).Day(), 12, 0, 0, 0, kst)).ForVisitor(context.Background())
		if idOf(inKST) != idOf(inUTC) || idOf(inKST) != idOf(sameDayNoon) {
			t.Fatalf("%s: KST=%s UTC표현=%s 같은날 정오=%s", instant.In(kst).Format("2006-01-02 15:04"), idOf(inKST), idOf(inUTC), idOf(sameDayNoon))
		}
	}
}

// 방문자 추첨이 고른 repo의 글은 그 repo의 오늘 글과 같아야 한다(발송 후에도).
func TestForVisitor_AfterSendShowsDeliveredContent(t *testing.T) {
	db := newTestDB(t)
	sub := insertConfirmedSubscriber(t, db, "a@example.com")
	insertRepo(t, db, "be")
	insertContent(t, db, "be", "0001")
	insertContent(t, db, "be", "0002")
	deliver(t, db, sub, "be", "0001", "2026-09-19 22:00:00")
	mustExec(t, db, `UPDATE contents SET rotation_count = 1 WHERE content_id = '0001'`)

	got, _ := newToday(db, time.Date(2026, 9, 20, 18, 0, 0, 0, kst)).ForVisitor(context.Background())
	if idOf(got) != "be/0001" {
		t.Errorf("got %s, want be/0001", idOf(got))
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
