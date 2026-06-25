package terminal

import (
	"context"
	"fmt"
	"strings"

	"github.com/maeilham/server/internal/delivery"
	gh "github.com/maeilham/server/internal/github"
	"github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/pkg/token"
	"github.com/maeilham/server/internal/store"
	"github.com/maeilham/server/internal/subscriber"
)

type RepoItem struct {
	Slug        string
	DisplayName string
}

type Service interface {
	ListActiveRepos(ctx context.Context) ([]*RepoItem, error)
	Subscribe(ctx context.Context, email string, repoWeights map[string]int) error
	Unsubscribe(ctx context.Context, tok string) error
	TodayContent(ctx context.Context) (*ContentItem, error)
	ListContents(ctx context.Context, limit int) ([]*ContentItem, error)
	GetContent(ctx context.Context, contentID string) (*ContentItem, error)
	EnsureDiscussion(ctx context.Context, contentID string) (string, error)
}

type termService struct {
	subStore     *subscriber.Store
	repoStore    store.RepoRepository
	contentStore store.ContentRepository
	mailer       mail.Mailer
	ghApp        *gh.App
	secret       string
	apiURL       string
}

func NewService(
	subStore *subscriber.Store,
	repoStore store.RepoRepository,
	contentStore store.ContentRepository,
	mailer mail.Mailer,
	ghApp *gh.App,
	secret, apiURL string,
) Service {
	return &termService{
		subStore:     subStore,
		repoStore:    repoStore,
		contentStore: contentStore,
		mailer:       mailer,
		ghApp:        ghApp,
		secret:       secret,
		apiURL:       apiURL,
	}
}

func (s *termService) ListActiveRepos(ctx context.Context) ([]*RepoItem, error) {
	repos, err := s.repoStore.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active repos: %w", err)
	}
	out := make([]*RepoItem, len(repos))
	for i, r := range repos {
		out[i] = &RepoItem{Slug: r.Slug, DisplayName: r.DisplayName}
	}
	return out, nil
}

func (s *termService) Subscribe(ctx context.Context, email string, repoWeights map[string]int) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if err := s.subStore.Reactivate(ctx, email); err != nil {
		return err
	}
	if _, err := s.subStore.Upsert(ctx, email); err != nil {
		return err
	}
	tok := token.Make(email, s.secret)
	var parts []string
	for slug, w := range repoWeights {
		parts = append(parts, fmt.Sprintf("%s:%d", slug, w))
	}
	confirmURL := fmt.Sprintf("%s/api/confirm?token=%s&repos=%s", s.apiURL, tok, strings.Join(parts, ","))
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
	return s.subStore.Unsubscribe(ctx, email)
}

func (s *termService) TodayContent(ctx context.Context) (*ContentItem, error) {
	c, err := s.contentStore.Today(ctx)
	if err != nil || c == nil {
		return nil, err
	}
	return contentToItem(c), nil
}

func (s *termService) EnsureDiscussion(ctx context.Context, contentID string) (string, error) {
	return delivery.EnsureDiscussion(ctx, s.ghApp, s.contentStore, s.repoStore, contentID)
}

func (s *termService) ListContents(ctx context.Context, limit int) ([]*ContentItem, error) {
	items, err := s.contentStore.ListRecent(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*ContentItem, len(items))
	for i, c := range items {
		out[i] = contentToItem(c)
	}
	return out, nil
}

func (s *termService) GetContent(ctx context.Context, contentID string) (*ContentItem, error) {
	c, err := s.contentStore.GetByID(ctx, contentID)
	if err != nil || c == nil {
		return nil, err
	}
	return contentToItem(c), nil
}

func contentToItem(c *store.Content) *ContentItem {
	return &ContentItem{
		RepoSlug:      c.RepoSlug,
		ContentID:     c.ContentID,
		Title:         c.Title,
		Preview:       c.Preview,
		GitHubURL:     c.GitHubURL,
		BodyPath:      c.BodyPath,
		DiscussionURL: c.DiscussionURL,
	}
}
