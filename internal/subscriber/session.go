package subscriber

import (
	"context"
	"errors"

	"github.com/maeilham/server/internal/pkg/token"
	"github.com/maeilham/server/internal/store"
)

// ErrUnauthorized는 개인 링크 토큰이 형식에 안 맞거나, 모르는 토큰이거나, 해지한 구독자의 것일 때 쓴다.
// HTTP 계층은 이 하나를 401로만 매핑하고, 어느 경우인지는 응답에 드러내지 않는다.
var ErrUnauthorized = errors.New("unauthorized")

// EstablishSession은 tok을 검증하고, 처음 여는 링크면 구독자를 확인 처리하고 전체 활성 repo에
// 구독시킨다. 멱등하다: 이미 확인된 구독자가 같은 링크를 다시 열어도 구독을 지우고 다시 만들지
// 않고 wasNewlyConfirmed=false만 돌려준다(기존 Confirm과 달리 옛 구독을 건드리지 않는다).
func (s *SubscriberService) EstablishSession(ctx context.Context, tok string) (wasNewlyConfirmed bool, err error) {
	if !token.IsAccessToken(tok) {
		return false, ErrUnauthorized
	}
	var newly bool
	err = s.repo.WithTx(ctx, func(tx store.SubscriberRepository) error {
		id, wasNew, err := tx.ConfirmByAccessToken(ctx, tok)
		if errors.Is(err, store.ErrSubscriberNotFound) {
			return ErrUnauthorized
		}
		if err != nil {
			return err
		}
		newly = wasNew
		if wasNew {
			return tx.AddAllActiveRepoSubscriptions(ctx, id)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return newly, nil
}

// SessionStatus는 tok을 검증만 한다(부작용 없음). 아직 확인되지 않은 구독자는 401로 취급한다.
// 실제 웹 흐름에서는 EstablishSession이 먼저 성공해야 토큰이 브라우저에 저장되므로, 정상적인
// 경우라면 여기 걸릴 일이 없다.
//
// paused_at은 여기서도 보지 않는다(EstablishSession, SubscriberByAccessToken과 같은 가정 —
// 일시정지는 발송 대상 선정에만 쓰는 값이라는 전제. 다르게 정해지면 store 계층만 고치면 된다).
func (s *SubscriberService) SessionStatus(ctx context.Context, tok string) error {
	if !token.IsAccessToken(tok) {
		return ErrUnauthorized
	}
	sess, err := s.repo.SubscriberByAccessToken(ctx, tok)
	if errors.Is(err, store.ErrSubscriberNotFound) {
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	if !sess.Confirmed {
		return ErrUnauthorized
	}
	return nil
}
