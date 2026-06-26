// Package delivery는 매일 발송할 콘텐츠를 선택하는 로직을 담는다.
//
// 핵심 원칙: 같은 repo를 구독한 사용자는 같은 날 같은 콘텐츠를 받는다 (뉴스 모델).
//   - 각 repo의 "오늘의 콘텐츠"는 rotation_count, content_id 오름차순 1개로 결정됨
//   - 사용자는 구독한 repo 중 가중치 확률로 1개 repo를 뽑고, 그 repo의 오늘 콘텐츠를 받음
//   - 가중 추첨은 (subscriber, day) 시드로 결정론적이라 재시도 시 동일 결과를 보장
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

// PickForSubscriber chooses which today's content to deliver to subscriberID.
//
// Rules:
//   - paused / unsubscribed subscribers → returns (nil, nil)
//   - subscriber with no active subscriptions → (nil, nil)
//   - subscriber whose subscribed repos have no eligible content → (nil, nil)
//   - otherwise picks one repo proportionally to subscription weights, then
//     returns that repo's today's content
//
// The pick is deterministic for a given (subscriberID, today): retries on the
// same day return the same content. This makes retries idempotent.
func PickForSubscriber(
	ctx context.Context,
	subRepo store.SubscriberRepository,
	contentStore store.ContentRepository,
	subscriberID int64,
	today time.Time,
) (*store.Content, error) {
	active, err := subRepo.IsActive(ctx, subscriberID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, nil
	}

	subs, err := subRepo.LoadSubscriptions(ctx, subscriberID)
	if err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return nil, nil
	}

	type candidate struct {
		weight  int
		content *store.Content
	}
	candidates := make([]candidate, 0, len(subs))
	for _, s := range subs {
		c, err := contentStore.TodayForRepo(ctx, s.RepoSlug)
		if err != nil {
			return nil, fmt.Errorf("today content for %s: %w", s.RepoSlug, err)
		}
		if c == nil {
			continue
		}
		candidates = append(candidates, candidate{weight: s.Weight, content: c})
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	totalWeight := 0
	for _, c := range candidates {
		totalWeight += c.weight
	}

	seed := dailyPickSeed(subscriberID, today)
	target := seed % uint64(totalWeight)
	acc := uint64(0)
	for _, c := range candidates {
		acc += uint64(c.weight)
		if target < acc {
			return c.content, nil
		}
	}
	return candidates[len(candidates)-1].content, nil
}

// dailyPickSeed returns a deterministic seed for (subscriberID, today).
// SHA-256 keeps the distribution uniform across small input ranges.
func dailyPickSeed(subscriberID int64, today time.Time) uint64 {
	key := strconv.FormatInt(subscriberID, 10) + "|" + today.Format("2006-01-02")
	sum := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint64(sum[:8])
}
