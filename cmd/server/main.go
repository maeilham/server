package main

import (
	"context"
	"errors"
	stdhttp "net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/delivery"
	gh "github.com/maeilham/server/internal/github"
	httpsrv "github.com/maeilham/server/internal/http"
	"github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/pkg/config"
	"github.com/maeilham/server/internal/pkg/logger"
	"github.com/maeilham/server/internal/subscriber"
	"github.com/maeilham/server/internal/terminal"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	log := logger.New(cfg.LogLevel)

	conn, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		log.Error("db open", "err", err)
		os.Exit(1)
	}
	defer conn.Close()
	if err := db.Migrate(conn); err != nil {
		log.Error("db migrate", "err", err)
		os.Exit(1)
	}
	log.Info("db ready", "dsn", cfg.DatabaseURL)

	mailer := mail.New(log, cfg.ResendAPIKey, cfg.MailFromEmail, cfg.MailFromName)
	store := subscriber.NewStore(conn)

	var ghApp *gh.App
	if cfg.GitHubAppID != 0 && cfg.GitHubAppPemPath != "" && cfg.GitHubInstallationID != 0 {
		if app, appErr := gh.NewApp(cfg.GitHubAppID, cfg.GitHubInstallationID, cfg.GitHubAppPemPath); appErr != nil {
			log.Warn("github app init failed (discussion 생성 비활성화)", "err", appErr)
		} else {
			ghApp = app
		}
	}

	termHandler := terminal.NewHandler(terminal.Deps{
		Subscribe: func(ctx context.Context, email string) error {
			email = strings.TrimSpace(strings.ToLower(email))
			if err := store.Reactivate(ctx, email); err != nil {
				return err
			}
			if _, err := store.Upsert(ctx, email); err != nil {
				return err
			}
			token := httpsrv.MakeToken(email, cfg.Secret)
			confirmURL := cfg.APIURL + "/api/confirm?token=" + token
			subject, text, html := mail.RenderConfirm(confirmURL)
			return mailer.Send(ctx, mail.Message{
				To:       email,
				Subject:  subject,
				TextBody: text,
				HTMLBody:  html,
			})
		},
		Unsubscribe: func(ctx context.Context, token string) error {
			email, err := httpsrv.VerifyToken(token, cfg.Secret)
			if err != nil {
				return err
			}
			return store.Unsubscribe(ctx, email)
		},
		TodayContent: func(ctx context.Context) (*terminal.ContentItem, error) {
			c, err := delivery.TodayContent(ctx, conn)
			if err != nil || c == nil {
				return nil, err
			}
			item := &terminal.ContentItem{
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
		},
		EnsureDiscussion: func(ctx context.Context, contentID string) (string, error) {
			url, err := delivery.EnsureDiscussion(ctx, ghApp, conn, contentID)
			if err != nil {
				log.Warn("discussion 생성 실패", "content_id", contentID, "err", err)
			}
			return url, err
		},
		ListContents: func(ctx context.Context, limit int) ([]*terminal.ContentItem, error) {
			items, err := delivery.ListContents(ctx, conn, limit)
			if err != nil {
				return nil, err
			}
			out := make([]*terminal.ContentItem, len(items))
			for i, c := range items {
				out[i] = &terminal.ContentItem{
					ContentID: c.ContentID,
					Title:     c.Title,
					Preview:   c.Preview,
					GitHubURL: c.GitHubURL,
					BodyPath:  c.BodyPath,
				}
			}
			return out, nil
		},
		GetContent: func(ctx context.Context, contentID string) (*terminal.ContentItem, error) {
			c, err := delivery.GetContent(ctx, conn, contentID)
			if err != nil || c == nil {
				return nil, err
			}
			return &terminal.ContentItem{
				ContentID: c.ContentID,
				Title:     c.Title,
				Preview:   c.Preview,
				GitHubURL: c.GitHubURL,
				BodyPath:  c.BodyPath,
			}, nil
		},
	})

	// SSH 서버 시작
	sshSrv, err := terminal.NewServer(log, termHandler)
	if err != nil {
		log.Error("ssh server init", "err", err)
		os.Exit(1)
	}
	go func() {
		if err := sshSrv.ListenAndServe(cfg.SSHAddr); err != nil {
			log.Error("ssh server", "err", err)
		}
	}()

	srv := &stdhttp.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpsrv.NewRouter(httpsrv.Deps{
			Logger:  log,
			Store:   store,
			Mailer:  mailer,
			BaseURL: cfg.BaseURL,
			APIURL:  cfg.APIURL,
			Secret:  cfg.Secret,
			SSHAddr: cfg.SSHAddr,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("server starting", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("server shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error", "err", err)
	}
}
