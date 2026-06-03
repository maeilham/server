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
	httpsrv "github.com/maeilham/server/internal/http"
	"github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/pkg/config"
	"github.com/maeilham/server/internal/pkg/logger"
	"github.com/maeilham/server/internal/subscriber"
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

	srv := &stdhttp.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpsrv.NewRouter(httpsrv.Deps{
			Logger:  log,
			Store:   subscriber.NewStore(conn),
			Mailer:  mailer,
			BaseURL: cfg.BaseURL,
			Secret:  cfg.Secret,
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
