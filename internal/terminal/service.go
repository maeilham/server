package terminal

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/maeilham/server/internal/delivery"
	gh "github.com/maeilham/server/internal/github"
	"github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/pkg/token"
	"github.com/maeilham/server/internal/subscriber"
)

type Service interface {
	Subscribe(ctx context.Context, email string) error
	Unsubscribe(ctx context.Context, tok string) error
	TodayContent(ctx context.Context) (*ContentItem, error)
	ListContents(ctx context.Context, limit int) ([]*ContentItem, error)
	GetContent(ctx context.Context, contentID string) (*ContentItem, error)
	EnsureDiscussion(ctx context.Context, contentID string) (string, error)
}

type termService struct {
	db     *sql.DB
	store  *subscriber.Store
	mailer mail.Mailer
	ghApp  *gh.App
	secret string
	apiURL string
}

func NewService(db *sql.DB, store *subscriber.Store, mailer mail.Mailer, ghApp *gh.App, secret, apiURL string) Service {
	return &termService{db: db, store: store, mailer: mailer, ghApp: ghApp, secret: secret, apiURL: apiURL}
}

func (s *termService) Subscribe(ctx context.Context, email string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if err := s.store.Reactivate(ctx, email); err != nil {
		return err
	}
	if _, err := s.store.Upsert(ctx, email); err != nil {
		return err
	}
	tok := token.Make(email, s.secret)
	confirmURL := fmt.Sprintf("%s/api/confirm?token=%s", s.apiURL, tok)
	subject, text, html := mail.RenderConfirm(confirmURL)
	return s.mailer.Send(ctx, mail.Message{
		To:       email,
		Subject:  subject,
		TextBody: text,
		HTMLBody:  html,
	})
}

func (s *termService) Unsubscribe(ctx context.Context, tok string) error {
	email, err := token.Verify(tok, s.secret)
	if err != nil {
		return err
	}
	return s.store.Unsubscribe(ctx, email)
}

func (s *termService) TodayContent(ctx context.Context) (*ContentItem, error) {
	c, err := delivery.TodayContent(ctx, s.db)
	if err != nil || c == nil {
		return nil, err
	}
	item := &ContentItem{
		ContentID: c.ContentID,
		Title:     c.Title,
		Preview:   c.Preview,
		GitHubURL: c.GitHubURL,
		BodyPath:  c.BodyPath,
	}
	if c.DiscussionURL.Valid {
		item.DiscussionURL = c.DiscussionURL.String
	}
	return item, nil
}

func (s *termService) EnsureDiscussion(ctx context.Context, contentID string) (string, error) {
	return delivery.EnsureDiscussion(ctx, s.ghApp, s.db, contentID)
}

func (s *termService) ListContents(ctx context.Context, limit int) ([]*ContentItem, error) {
	items, err := delivery.ListContents(ctx, s.db, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*ContentItem, len(items))
	for i, c := range items {
		out[i] = &ContentItem{
			RepoSlug:  c.RepoSlug,
			ContentID: c.ContentID,
			Title:     c.Title,
			Preview:   c.Preview,
			GitHubURL: c.GitHubURL,
			BodyPath:  c.BodyPath,
		}
	}
	return out, nil
}

func (s *termService) GetContent(ctx context.Context, contentID string) (*ContentItem, error) {
	c, err := delivery.GetContent(ctx, s.db, contentID)
	if err != nil || c == nil {
		return nil, err
	}
	return &ContentItem{
		ContentID: c.ContentID,
		Title:     c.Title,
		Preview:   c.Preview,
		GitHubURL: c.GitHubURL,
		BodyPath:  c.BodyPath,
	}, nil
}
