package main

import (
	"context"
	"errors"
	stdhttp "net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/maeilham/server/internal/db"
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

	termSvc := terminal.NewService(conn, store, mailer, ghApp, cfg.Secret, cfg.APIURL)
	termHandler := terminal.NewHandler(termSvc)

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
