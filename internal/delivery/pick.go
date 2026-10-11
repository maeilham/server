// Package delivery는 오늘의 질문을 정하고(Picker) 매일 메일을 발송하는(DailySend) 로직을 담는다.
//
// 핵심 원칙: 같은 repo를 구독한 사용자는 같은 날 같은 콘텐츠를 받는다 (뉴스 모델).
//   - 각 repo의 "오늘의 콘텐츠"는 Picker.ContentOfDay 하나로 결정된다
//   - 사용자는 구독한 repo 중 가중치 확률로 1개 repo를 뽑고, 그 repo의 오늘 콘텐츠를 받음
//   - 가중 추첨은 (구독자, 서비스의 날짜) 시드로 결정론적이라 재시도 시 동일 결과를 보장
package delivery

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"time"
)

// pickByWeight는 seed에 따라 가중치에 비례해 인덱스 하나를 고른다.
// (subscriber, day) 시드를 쓰면 같은 입력에 항상 같은 결과가 나온다.
// weights는 비어 있지 않고 합이 0보다 커야 한다 (구독 가중치는 DB에서 1~5로 제한됨).
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

// dailyPickSeed returns a deterministic seed for (subscriberID, today).
// SHA-256 keeps the distribution uniform across small input ranges.
//
// today의 달력 날짜가 시드이므로 서비스 시간대(MAEILHAM_TZ)로 바꾼 시각을 넘겨야 한다(Picker.pick이 그렇게 한다).
func dailyPickSeed(subscriberID int64, today time.Time) uint64 {
	key := strconv.FormatInt(subscriberID, 10) + "|" + today.Format("2006-01-02")
	sum := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint64(sum[:8])
}
