package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/alecthomas/kong"
	"github.com/maeilham/server/internal/db"
	gh "github.com/maeilham/server/internal/github"
	"github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/pkg/config"
	"github.com/maeilham/server/internal/pkg/logger"
	"github.com/maeilham/server/internal/store"
)

var CLI struct {
	Serve    ServeCmd    `cmd:"" help:"HTTP 서버 기동"`
	Sync     SyncCmd     `cmd:"" help:"콘텐츠 repo를 fetch + DB로 sync"`
	Notify   NotifyCmd   `cmd:"" help:"오늘 처음 나온 새 질문을 구독자에게 메일로 알림"`
	SendTest SendTestCmd `cmd:"send-test" help:"발송 어댑터 동작 확인용 메일 1통 전송"`
	Repo     RepoCmd     `cmd:"" help:"repo 관리"`
	GenLink  GenLinkCmd  `cmd:"gen-link" help:"테스트용 링크 생성"`
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
		cfg:           cfg,
		log:           log,
		repoStore:     store.NewRepoStore(conn),
		contentStore:  store.NewContentStore(conn),
		logStore:      store.NewDeliveryLogStore(conn),
		questionStore: store.NewDailyQuestionStore(conn),
		subRepo:       store.NewSubscriberStore(conn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	k := kong.Parse(&CLI,
		kong.Name("maeilham"),
		kong.UsageOnError(),
		kong.BindTo(ctx, (*context.Context)(nil)),
		kong.Bind(d),
	)
	k.FatalIfErrorf(k.Run())
}

type deps struct {
	cfg           *config.Config
	log           *slog.Logger
	repoStore     store.RepoRepository
	contentStore  store.ContentRepository
	logStore      store.DeliveryLogRepository
	questionStore store.DailyQuestionRepository
	subRepo       store.SubscriberRepository

	appOnce sync.Once
	app     *gh.App
}

// location은 서비스의 "하루"를 나누는 시간대(MAEILHAM_TZ)를 돌려준다. serve와 send-daily가 같은 값을 쓴다.
func (d *deps) location() (*time.Location, error) {
	loc, err := time.LoadLocation(d.cfg.TimeZone)
	if err != nil {
		return nil, fmt.Errorf("MAEILHAM_TZ %q: %w", d.cfg.TimeZone, err)
	}
	return loc, nil
}

// ghApp은 GitHub App 클라이언트를 한 번만 만들어 돌려준다(설정이 없거나 만들기에 실패하면 nil).
// 스케줄러가 sync를 되풀이해도 PEM을 다시 읽거나 발급받은 installation 토큰 캐시를 버리지 않는다.
func (d *deps) ghApp() *gh.App {
	d.appOnce.Do(func() {
		cfg := d.cfg
		if cfg.GitHubAppID == 0 || cfg.GitHubAppPemPath == "" || cfg.GitHubInstallationID == 0 {
			return
		}
		app, err := gh.NewApp(cfg.GitHubAppID, cfg.GitHubInstallationID, cfg.GitHubAppPemPath)
		if err != nil {
			d.log.Warn("github app init failed", "err", err)
			return
		}
		d.app = app
	})
	return d.app
}

func (d *deps) mailer() mail.Mailer {
	return mail.New(d.log, d.cfg.ResendAPIKey, d.cfg.MailFromEmail, d.cfg.MailFromName)
}
