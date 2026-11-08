package main

import (
	"context"
	"errors"
	stdhttp "net/http"
	"os"
	"time"

	"github.com/maeilham/server/internal/content"
	"github.com/maeilham/server/internal/delivery"
	httpsrv "github.com/maeilham/server/internal/http"
	"github.com/maeilham/server/internal/subscriber"
)

// notifyCheckInterval마다 알림 시각이 지났는지 보고, 아직 알리지 않은 새 질문이 있으면 보낸다.
const notifyCheckInterval = 5 * time.Minute

type ServeCmd struct{}

func (c *ServeCmd) Run(ctx context.Context, d *deps) error {
	loc, err := d.location()
	if err != nil {
		return err
	}

	subSvc := subscriber.NewSubscriberService(d.subRepo, d.mailer(), d.cfg.Secret, d.cfg.APIURL, d.cfg.BaseURL)

	srv := &stdhttp.Server{
		Addr: d.cfg.HTTPAddr,
		Handler: httpsrv.NewRouter(httpsrv.Deps{
			Logger:  d.log,
			SubSvc:  subSvc,
			BaseURL: d.cfg.BaseURL,

			Contents: d.contentStore,
			Bodies: content.NewCachedBodySource(
				content.NewGitHubClient(d.cfg.GitHubToken),
				content.BodyOptions{Logger: d.log},
			),
			Today: &delivery.Picker{Repos: d.repoStore, Subs: d.subRepo, Contents: d.contentStore, Questions: d.questionStore, Loc: loc},
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	if d.cfg.SyncInterval > 0 {
		go runEvery(ctx, d.cfg.SyncInterval, func(ctx context.Context) {
			if err := syncRepos(ctx, d, ""); err != nil {
				d.log.Error("scheduled sync", "err", err)
			}
		})
	}

	if d.cfg.NotifyEnabled {
		go runEvery(ctx, notifyCheckInterval, func(ctx context.Context) {
			now := time.Now()
			if !delivery.NotifyDue(now, loc, d.cfg.NotifyAt) {
				return
			}
			if err := notifyNewQuestions(ctx, d, loc, now, false, ""); err != nil {
				d.log.Error("scheduled notify", "err", err)
			}
		})
	}

	go func() {
		d.log.Info("server starting", "addr", d.cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			d.log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	d.log.Info("server shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
