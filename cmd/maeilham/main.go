package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/maeilham/server/internal/db"
	gh "github.com/maeilham/server/internal/github"
	"github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/pkg/config"
	"github.com/maeilham/server/internal/pkg/logger"
	"github.com/maeilham/server/internal/store"
)

var CLI struct {
	Serve     ServeCmd     `cmd:"" help:"HTTP + SSH 서버 기동"`
	Sync      SyncCmd      `cmd:"" help:"콘텐츠 repo를 fetch + DB로 sync"`
	SendDaily SendDailyCmd `cmd:"send-daily" help:"활성 구독자 전체에 오늘의 메일 발송"`
	SendTest  SendTestCmd  `cmd:"send-test" help:"발송 어댑터 동작 확인용 메일 1통 전송"`
	Repo      RepoCmd      `cmd:"" help:"repo 관리"`
	GenLink   GenLinkCmd   `cmd:"gen-link" help:"테스트용 링크 생성"`
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
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

	d := &deps{
		cfg:          cfg,
		log:          log,
		repoStore:    store.NewRepoStore(conn),
		contentStore: store.NewContentStore(conn),
		logStore:     store.NewDeliveryLogStore(conn),
		subRepo:      store.NewSubscriberStore(conn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	k := kong.Parse(&CLI,
		kong.Name("maeilham"),
		kong.UsageOnError(),
	)
	k.FatalIfErrorf(k.Run(ctx, d))
}

type deps struct {
	cfg          *config.Config
	log          *slog.Logger
	repoStore    store.RepoRepository
	contentStore store.ContentRepository
	logStore     store.DeliveryLogRepository
	subRepo      store.SubscriberRepository
}

func (d *deps) ghApp() *gh.App {
	cfg := d.cfg
	if cfg.GitHubAppID == 0 || cfg.GitHubAppPemPath == "" || cfg.GitHubInstallationID == 0 {
		return nil
	}
	app, err := gh.NewApp(cfg.GitHubAppID, cfg.GitHubInstallationID, cfg.GitHubAppPemPath)
	if err != nil {
		d.log.Warn("github app init failed", "err", err)
		return nil
	}
	return app
}

func (d *deps) mailer() mail.Mailer {
	return mail.New(d.log, d.cfg.ResendAPIKey, d.cfg.MailFromEmail, d.cfg.MailFromName)
}
