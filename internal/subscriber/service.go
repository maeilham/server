package subscriber

import (
	"context"
	"fmt"
	"strings"

	imail "github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/pkg/token"
	"github.com/maeilham/server/internal/store"
)

type SubscriberService struct {
	repo    store.SubscriberRepository
	mailer  imail.Mailer
	secret  string
	apiURL  string // API 서버 주소. 옛 확인 링크(/api/confirm)에 쓴다
	baseURL string // 웹 주소. 개인 링크(<웹>/#t=<토큰>)에 쓴다
}

func NewSubscriberService(repo store.SubscriberRepository, mailer imail.Mailer, secret, apiURL, baseURL string) *SubscriberService {
	return &SubscriberService{repo: repo, mailer: mailer, secret: secret, apiURL: apiURL, baseURL: baseURL}
}

// PersonalLinkURL은 토큰으로 개인 링크를 만든다. 토큰은 '#' 뒤에 두어서 서버 로그나 리퍼러로 새지 않는다.
func (s *SubscriberService) PersonalLinkURL(accessToken string) string {
	return strings.TrimSuffix(s.baseURL, "/") + "/#t=" + accessToken
}

// Subscribe reactivates or creates a subscriber and emails them their personal link.
//
// 이 메일 하나가 가입 확인과 개인 링크를 겸한다(링크를 처음 여는 것이 이메일 인증).
// 이미 가입한 주소로 다시 불러도 같은 토큰이라 같은 링크를 다시 보낸다(링크를 잃어버린 경우).
//
// repoWeights가 있으면(터미널의 repo 선택 흐름) 가중치를 확인 링크에 실어야 하므로 옛 확인 메일을 쓴다.
func (s *SubscriberService) Subscribe(ctx context.Context, email string, repoWeights map[string]int) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if err := s.repo.Reactivate(ctx, email); err != nil {
		return err
	}
	id, err := s.repo.Upsert(ctx, email)
	if err != nil {
		return err
	}

	var subject, text, html string
	if len(repoWeights) > 0 {
		confirmURL := s.buildConfirmURL(token.Make(email, s.secret), repoWeights)
		subject, text, html = imail.RenderConfirm(confirmURL)
	} else {
		accessToken, err := s.repo.EnsureAccessToken(ctx, id)
		if err != nil {
			return err
		}
		subject, text, html = imail.RenderLink(s.PersonalLinkURL(accessToken))
	}
	return s.mailer.Send(ctx, imail.Message{
		To:       email,
		Subject:  subject,
		TextBody: text,
		HTMLBody: html,
	})
}

// Confirm verifies tok, then atomically sets confirmed_at and writes subscriptions.
func (s *SubscriberService) Confirm(ctx context.Context, tok string, repoWeights map[string]int) error {
	email, err := token.Verify(tok, s.secret)
	if err != nil {
		return err
	}
	return s.repo.WithTx(ctx, func(tx store.SubscriberRepository) error {
		id, err := tx.SetConfirmed(ctx, email)
		if err != nil {
			return err
		}
		if err := tx.ClearSubscriptions(ctx, id); err != nil {
			return err
		}
		if len(repoWeights) > 0 {
			for slug, w := range repoWeights {
				if err := tx.AddSubscription(ctx, id, slug, w); err != nil {
					return err
				}
			}
		} else {
			if err := tx.AddAllActiveRepoSubscriptions(ctx, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Unsubscribe verifies tok and marks the subscriber as unsubscribed.
func (s *SubscriberService) Unsubscribe(ctx context.Context, tok string) error {
	email, err := token.Verify(tok, s.secret)
	if err != nil {
		return err
	}
	return s.repo.Unsubscribe(ctx, email)
}

func (s *SubscriberService) buildConfirmURL(tok string, repoWeights map[string]int) string {
	if len(repoWeights) == 0 {
		return fmt.Sprintf("%s/api/confirm?token=%s", s.apiURL, tok)
	}
	parts := make([]string, 0, len(repoWeights))
	for slug, w := range repoWeights {
		parts = append(parts, fmt.Sprintf("%s:%d", slug, w))
	}
	return fmt.Sprintf("%s/api/confirm?token=%s&repos=%s", s.apiURL, tok, strings.Join(parts, ","))
}
