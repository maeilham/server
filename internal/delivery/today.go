package delivery

import (
	"context"
	"fmt"
	"time"

	"github.com/maeilham/server/internal/store"
)

// TodayService는 "오늘의 질문"을 정한다. 누구인지 모르는 방문자용이다.
//
// 오늘의 질문 = 오늘 발송해야 할 글이다. 메일 발송이 쓰는 선정 규칙을 읽기 전용으로 다시 쓴다.
//
// 로테이션은 "다음에 보낼 글"을 가리키는 표시다(TodayForRepo가 그 글을 돌려준다).
// 하루 발송이 끝나면 배치(AdvanceRotation)가 이 표시를 다음 글로 옮긴다.
//
//	06:00 발송 전   표시 → 0001            (오늘 보낼 글 = 0001)
//	07:00 발송      0001을 보내고, 끝나면 표시를 0002로 옮김
//	15:00 웹 접속   표시 → 0002, 오늘 나간 글 = 0001   ← 표시만 보면 내일 글이 보인다
//
// 그래서 규칙은 이렇다.
//
//	repo의 오늘 글 = 오늘 이미 나간 글이 있으면 그 글, 없으면 표시가 가리키는 글(TodayForRepo)
//
// 메일 배치는 발송 직전에 한 번만 읽어서 "발송 전"만 보지만, 웹은 하루 종일 읽어서
// 발송 전과 후를 모두 만난다. 그래서 "오늘 이미 나간 글"을 먼저 확인한다.
type TodayService struct {
	Repos    store.RepoRepository
	Contents store.ContentRepository
	Log      store.DeliveryLogRepository
	Loc      *time.Location   // 서비스의 하루를 나누는 시간대 (운영 기본 Asia/Seoul)
	Now      func() time.Time // 테스트에서 시간을 제어하기 위한 주입점. nil이면 time.Now
}

func (s *TodayService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ForVisitor returns "오늘의 질문" 하나. 발송할 글이 어떤 repo에도 없으면 (nil, nil).
//
// 방문자는 구독 정보가 없으므로 "활성 repo를 모두 같은 가중치로 구독한 가상의 구독자"로 보고,
// 메일과 같은 결정적 가중 추첨(pickByWeight, dailyPickSeed)으로 오늘 어느 repo의 글을 보여줄지 정한다.
// 시드는 subscriberID 0(실제 구독자 id는 1부터)과 서비스 시간대의 날짜라서, 같은 날은 누가 물어도 같은 결과다.
func (s *TodayService) ForVisitor(ctx context.Context) (*store.Content, error) {
	repos, err := s.Repos.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active repos: %w", err)
	}

	var candidates []*store.Content
	for _, r := range repos { // ListActive는 slug 순이라 후보 순서가 안정적이다
		c, err := s.ContentOfDay(ctx, r.Slug)
		if err != nil {
			return nil, err
		}
		if c != nil {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	weights := make([]int, len(candidates))
	for i := range weights {
		weights[i] = 1
	}
	day := s.now().In(s.Loc)
	return candidates[pickByWeight(weights, dailyPickSeed(0, day))], nil
}

// ContentOfDay returns 그 repo의 오늘 글. repo에 글이 하나도 없으면 (nil, nil).
func (s *TodayService) ContentOfDay(ctx context.Context, repoSlug string) (*store.Content, error) {
	now := s.now().In(s.Loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.Loc)
	end := start.AddDate(0, 0, 1)

	// 1) 오늘 이미 나간 글이 있으면 그 글. 발송 기록은 UTC로 저장돼 있으므로 하루를 시각 구간으로 비교한다.
	if id, found, err := s.Log.LastDeliveredContentID(ctx, repoSlug, start, end); err != nil {
		return nil, err
	} else if found {
		c, err := s.Contents.GetByRepoAndID(ctx, repoSlug, id)
		if err != nil {
			return nil, err
		}
		if c != nil {
			return c, nil
		}
		// 오늘 나간 글이 그 뒤에 삭제됐다면 아래 로테이션의 다음 글로 대체한다.
	}

	// 2) 아직 오늘 발송 전이면 메일이 쓰는 바로 그 함수로 다음 글을 고른다.
	next, err := s.Contents.TodayForRepo(ctx, repoSlug)
	if err != nil || next == nil {
		return nil, err
	}
	// TodayForRepo는 일부 필드만 채우므로, 응답에 필요한 전체 행(repo 이름, 태그 등)을 다시 읽는다.
	return s.Contents.GetByRepoAndID(ctx, repoSlug, next.ContentID)
}
