// Package delivery는 오늘의 질문을 정하고(Picker) 매일 메일을 발송하는(DailySend) 로직을 담음.
//
// 핵심 원칙: 같은 repo를 구독한 사용자는 같은 날 같은 콘텐츠를 받음(뉴스 모델).
//   - 각 repo의 "오늘의 콘텐츠"는 Picker.contentOfDay 하나로 결정됨
//   - 사용자는 구독한 repo 중 가중치 확률로 1개 repo를 뽑고, 그 repo의 오늘 콘텐츠를 받음
//   - 가중 추첨은 (구독자, 서비스의 날짜) 시드로 결정론적이라 재시도 시 동일 결과를 보장함
package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
	"time"

	"github.com/maeilham/server/internal/store"
)

// Picker는 "오늘의 질문" pick해요
//
//	ForGuest       구독 정보가 없는 사람(게스트). 활성 repo 전체를 같은 가중치로 봄
//	ForSubscriber  구독한 repo가 있는 사람. 구독 repo와 구독자별 가중치로 봄
//
// repo의 오늘 글 = 오늘 이미 나간 글이 있으면 그 글, 없으면 표시가 가리키는 글(TodayForRepo)
//
// 발송 직전에만 읽는 배치와 달리 API, 같은 날 재실행, 뒤늦게 가입한 구독자는 발송 전과 후를 모두 만남.
// 모두 이 규칙을 사용하므로 같은 repo의 같은 날은 누구에게나 같은 글임.
type Picker struct {
	Repos    store.RepoRepository
	Subs     store.SubscriberRepository
	Contents store.ContentRepository
	Log      store.DeliveryLogRepository
	Loc      *time.Location   // 서비스의 하루를 나누는 시간대 (운영 기본값 Asia/Seoul)
	Now      func() time.Time // 기준 시각. 테스트와 메일 배치(발송 기준 시각)가 고정하는 주입점. nil이면 time.Now 사용
}

func (p *Picker) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

type repoWeight struct {
	slug   string
	weight int
}

// ForGuest는 게스트의 오늘의 질문 하나를 돌려줘요
// 게스트는 구독 정보가 없으므로 "활성 repo를 모두 같은 가중치로 구독한 가상의 구독자(id 0)"로 취급함.
// 시드는 id 0(실제 구독자 id는 1부터)과 서비스 시간대의 날짜이므로, 같은 날은 누가 물어도 같은 결과임.
func (p *Picker) ForGuest(ctx context.Context) (*store.Content, error) {
	repos, err := p.Repos.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active repos: %w", err)
	}
	candidates := make([]repoWeight, 0, len(repos)) // ListActive가 slug 순
	for _, r := range repos {
		candidates = append(candidates, repoWeight{slug: r.Slug, weight: 1})
	}
	return p.pick(ctx, 0, candidates)
}

// ForSubscriber는 subscriberID의 오늘의 질문 하나를 돌려줘요.
//
//   - 일시정지 / 해지 / 미확인 구독자 → (nil, nil)
//   - 활성 구독이 없거나, 구독한 repo에 보낼 글이 없으면 → (nil, nil)
//   - 그 외에는 구독 가중치에 비례해 repo 하나를 뽑고 그 repo의 오늘 글을 돌려줌
//
// 같은 (구독자, 서비스의 하루)에는 항상 같은 결과임(재시도해도 같은 글).
func (p *Picker) ForSubscriber(ctx context.Context, subscriberID int64) (*store.Content, error) {
	active, err := p.Subs.IsActive(ctx, subscriberID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, nil
	}
	subs, err := p.Subs.LoadSubscriptions(ctx, subscriberID) // repo slug 순
	if err != nil {
		return nil, err
	}
	candidates := make([]repoWeight, 0, len(subs))
	for _, s := range subs {
		candidates = append(candidates, repoWeight{slug: s.RepoSlug, weight: s.Weight})
	}
	return p.pick(ctx, subscriberID, candidates)
}

// pick는 후보 repo마다 그날 글을 구하고(글이 없는 repo는 제외), (seedID, 서비스의 날짜) 시드로 하나를 고름.
func (p *Picker) pick(ctx context.Context, seedID int64, repos []repoWeight) (*store.Content, error) {
	type candidate struct {
		weight  int
		content *store.Content
	}
	var candidates []candidate
	for _, r := range repos {
		c, err := p.contentOfDay(ctx, r.slug)
		if err != nil {
			return nil, fmt.Errorf("today content for %s: %w", r.slug, err)
		}
		if c != nil {
			candidates = append(candidates, candidate{weight: r.weight, content: c})
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	weights := make([]int, len(candidates))
	for i, c := range candidates {
		weights[i] = c.weight
	}
	day := p.now().In(p.Loc)
	return candidates[pickByWeight(weights, dailyPickSeed(seedID, day))].content, nil
}

// contentOfDay는 그 repo의 오늘 글을 돌려줌. repo에 글이 하나도 없으면 (nil, nil).
func (p *Picker) contentOfDay(ctx context.Context, repoSlug string) (*store.Content, error) {
	start, end := ServiceDay(p.now(), p.Loc)

	// 1) 오늘 이미 나간 글이 있으면 그 글을 사용함. 발송 기록은 UTC로 저장되어 있으므로 하루를 시각 구간으로 비교함.
	if id, found, err := p.Log.LastDeliveredContentID(ctx, repoSlug, start, end); err != nil {
		return nil, err
	} else if found {
		c, err := p.Contents.GetByRepoAndID(ctx, repoSlug, id)
		if err != nil {
			return nil, err
		}
		if c != nil {
			return c, nil
		}
		// 오늘 나간 글이 그 뒤에 삭제되었다면 아래 로테이션의 다음 글로 대체함.
	}

	// 2) 아직 오늘 발송 전이면 로테이션 표시가 가리키는 다음 글을 사용함.
	next, err := p.Contents.TodayForRepo(ctx, repoSlug)
	if err != nil || next == nil {
		return nil, err
	}
	// TodayForRepo는 일부 필드만 채우므로, 응답에 필요한 전체 행(repo 이름, 태그 등)을 다시 읽음.
	return p.Contents.GetByRepoAndID(ctx, repoSlug, next.ContentID)
}

// pickByWeight는 seed에 따라 가중치에 비례해 인덱스 하나를 고름.
// (구독자, 날짜) 시드를 사용하면 같은 입력에 항상 같은 결과가 나옴.
// weights는 비어 있지 않고 합이 0보다 커야 함(구독 가중치는 DB에서 1~5로 제한됨).
func pickByWeight(weights []int, seed uint64) int {
	total := 0
	for _, w := range weights {
		total += w
	}
	target := seed % uint64(total)
	acc := uint64(0)
	for i, w := range weights {
		acc += uint64(w)
		if target < acc {
			return i
		}
	}
	return len(weights) - 1
}

// dailyPickSeed는 (subscriberID, today)에 대한 결정적 시드를 돌려줌.
// SHA-256을 사용하므로 작은 입력 범위에서도 분포가 고르게 나옴.
//
// today의 달력 날짜가 시드이므로 서비스 시간대(MAEILHAM_TZ)로 바꾼 시각을 넘겨야 함(Picker.pick이 그렇게 함).
func dailyPickSeed(subscriberID int64, today time.Time) uint64 {
	key := strconv.FormatInt(subscriberID, 10) + "|" + today.Format("2006-01-02")
	sum := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint64(sum[:8])
}
