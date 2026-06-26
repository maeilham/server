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
	repo   store.SubscriberRepository
	mailer imail.Mailer
	secret string
	apiURL string
}

func NewSubscriberService(repo store.SubscriberRepository, mailer imail.Mailer, secret, apiURL string) *SubscriberService {
	return &SubscriberService{repo: repo, mailer: mailer, secret: secret, apiURL: apiURL}
}

// Subscribe reactivates or creates a subscriber and sends a confirmation email.
// repoWeights is encoded in the confirm URL for SSH-flow subscribers; empty = all active repos.
func (s *SubscriberService) Subscribe(ctx context.Context, email string, repoWeights map[string]int) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if err := s.repo.Reactivate(ctx, email); err != nil {
		return err
	}
	if _, err := s.repo.Upsert(ctx, email); err != nil {
		return err
	}
	tok := token.Make(email, s.secret)
	confirmURL := s.buildConfirmURL(tok, repoWeights)
	subject, text, html := imail.RenderConfirm(confirmURL)
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
