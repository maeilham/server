package main

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"os"
	"time"

	httpsrv "github.com/maeilham/server/internal/http"
	"github.com/maeilham/server/internal/subscriber"
	"github.com/maeilham/server/internal/terminal"
)

type ServeCmd struct{}

func (c *ServeCmd) Run(ctx context.Context, d *deps) error {
	subSvc := subscriber.NewSubscriberService(d.subRepo, d.mailer(), d.cfg.Secret, d.cfg.APIURL)

	termSvc := terminal.NewService(subSvc, d.repoStore, d.contentStore, d.ghApp())
	termHandler := terminal.NewHandler(termSvc)

	sshSrv, err := terminal.NewServer(d.log, termHandler)
	if err != nil {
		return fmt.Errorf("ssh server init: %w", err)
	}
	go func() {
		if err := sshSrv.ListenAndServe(d.cfg.SSHAddr); err != nil {
			d.log.Error("ssh server", "err", err)
		}
	}()

	srv := &stdhttp.Server{
		Addr: d.cfg.HTTPAddr,
		Handler: httpsrv.NewRouter(httpsrv.Deps{
			Logger:  d.log,
			SubSvc:  subSvc,
			BaseURL: d.cfg.BaseURL,
			SSHAddr: d.cfg.SSHAddr,
		}),
		ReadHeaderTimeout: 10 * time.Second,
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
