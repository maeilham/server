package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/maeilham/server/internal/content"
	"github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/delivery"
	gh "github.com/maeilham/server/internal/github"
	httpsrv "github.com/maeilham/server/internal/http"
	"github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/pkg/config"
	"github.com/maeilham/server/internal/pkg/logger"
	"github.com/maeilham/server/internal/store"
	"github.com/maeilham/server/internal/subscriber"
	"github.com/maeilham/server/internal/terminal"
)

// ── 최상위 CLI 구조 ──────────────────────────────────────────────────────────

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

	deps := &deps{
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
	k.FatalIfErrorf(k.Run(ctx, deps))
}

// ── 공유 의존성 ───────────────────────────────────────────────────────────────

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

// ── serve ────────────────────────────────────────────────────────────────────

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

// ── sync ─────────────────────────────────────────────────────────────────────

type SyncCmd struct {
	Repo string `short:"r" help:"sync할 repo slug (미지정 시 전체 active repo)"`
}

func (c *SyncCmd) Run(ctx context.Context, d *deps) error {
	repos, err := d.repoStore.ListActive(ctx)
	if err != nil {
		return err
	}
	if c.Repo != "" {
		filtered := repos[:0]
		for _, r := range repos {
			if r.Slug == c.Repo {
				filtered = append(filtered, r)
			}
		}
		repos = filtered
	}
	if len(repos) == 0 {
		d.log.Warn("no repos to sync")
		return nil
	}

	ghClient := content.NewGitHubClient(d.cfg.GitHubToken)
	ghApp := d.ghApp()

	for _, r := range repos {
		log := d.log.With("repo", r.Slug)
		log.Info("sync starting", "url", r.GitHubURL)
		stats, err := content.Sync(ctx, log, d.contentStore, ghClient, ghApp, r.Slug, r.GitHubURL, "")
		if err != nil {
			log.Error("sync", "err", err)
			continue
		}
		log.Info("sync done",
			"scanned", stats.Scanned, "inserted", stats.Inserted,
			"updated", stats.Updated, "deleted", stats.Deleted,
			"skipped", stats.Skipped, "errors", stats.Errors)
	}
	return nil
}

// ── send-daily ───────────────────────────────────────────────────────────────

type SendDailyCmd struct {
	DryRun  bool   `help:"결정만 로그하고 실제 발송/DB 변경은 하지 않음"`
	Date    string `help:"기준 날짜 YYYY-MM-DD (미지정 시 오늘)"`
	BaseURL string `help:"사이트 base URL" default:""`
}

func (c *SendDailyCmd) Run(ctx context.Context, d *deps) error {
	day := time.Now().UTC()
	if c.Date != "" {
		t, err := time.Parse("2006-01-02", c.Date)
		if err != nil {
			return fmt.Errorf("invalid --date: %w", err)
		}
		day = t
	}
	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = d.cfg.BaseURL
	}

	stats, err := delivery.DailySend(ctx, d.log, d.mailer(), delivery.DailySendOptions{
		Day:          day,
		DryRun:       c.DryRun,
		BaseURL:      baseURL,
		APIURL:       d.cfg.APIURL,
		Secret:       d.cfg.Secret,
		GitHubApp:    d.ghApp(),
		SubRepo:      d.subRepo,
		RepoStore:    d.repoStore,
		ContentStore: d.contentStore,
		LogStore:     d.logStore,
	})
	if err != nil {
		return err
	}
	d.log.Info("send-daily done",
		"dry_run", stats.DryRun,
		"subscribers", stats.Subscribers,
		"picked", stats.Picked,
		"sent", stats.Sent,
		"skipped_already_sent", stats.Skipped,
		"no_content", stats.NoContent,
		"errors", stats.Errors,
		"contents_advanced", stats.Advanced)
	return nil
}

// ── send-test ────────────────────────────────────────────────────────────────

type SendTestCmd struct {
	To string `required:"" help:"수신 이메일 주소"`
}

func (c *SendTestCmd) Run(ctx context.Context, d *deps) error {
	subject, text, html := mail.RenderDaily(mail.DailyMailData{
		RepoName:       "백엔드 면접",
		Title:          "인덱스(Index)가 무엇이고, 어떻게 동작하나요?",
		Preview:        "DB 인덱스는 데이터 검색 속도를 높이기 위해 별도로 관리하는 자료구조입니다.",
		GitHubURL:      "https://github.com/maeilham/be-interview/blob/main/content/0001-index.md",
		DiscussionURL:  "https://github.com/maeilham/be-interview/discussions/1",
		UnsubscribeURL: "https://maeilham.kr/unsubscribe?sid=0",
	})
	if err := d.mailer().Send(ctx, mail.Message{
		To: c.To, Subject: subject, TextBody: text, HTMLBody: html,
	}); err != nil {
		return err
	}
	d.log.Info("test mail dispatched", "to", c.To)
	return nil
}

// ── repo ─────────────────────────────────────────────────────────────────────

type RepoCmd struct {
	Add        RepoAddCmd        `cmd:"" help:"repo 등록/수정"`
	List       RepoListCmd       `cmd:"" help:"등록된 repo 목록"`
	Deactivate RepoDeactivateCmd `cmd:"" help:"repo 비활성화"`
}

type RepoAddCmd struct {
	Slug string `required:"" help:"레포 슬러그 (예: be-interview)"`
	URL  string `required:"" help:"GitHub URL"`
	Name string `required:"" help:"표시 이름"`
	Desc string `help:"설명 (선택)"`
}

func (c *RepoAddCmd) Run(ctx context.Context, d *deps) error {
	if err := d.repoStore.Upsert(ctx, &store.Repo{
		Slug:        c.Slug,
		GitHubURL:   c.URL,
		DisplayName: c.Name,
		Description: c.Desc,
	}); err != nil {
		return err
	}
	fmt.Printf("✓ repo 추가됨: %s\n", c.Slug)
	return nil
}

type RepoListCmd struct{}

func (c *RepoListCmd) Run(ctx context.Context, d *deps) error {
	repos, err := d.repoStore.ListAll(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%-25s %-10s %s\n", "SLUG", "ACTIVE", "URL")
	fmt.Println(strings.Repeat("-", 70))
	for _, r := range repos {
		status := "✓"
		if !r.Active {
			status = "✗"
		}
		fmt.Printf("%-25s %-10s %s  (%s)\n", r.Slug, status, r.GitHubURL, r.DisplayName)
	}
	return nil
}

type RepoDeactivateCmd struct {
	Slug string `required:"" help:"레포 슬러그"`
}

func (c *RepoDeactivateCmd) Run(ctx context.Context, d *deps) error {
	if err := d.repoStore.Deactivate(ctx, c.Slug); err != nil {
		return err
	}
	fmt.Printf("✓ repo 비활성화됨: %s\n", c.Slug)
	return nil
}

// ── gen-link ─────────────────────────────────────────────────────────────────

type GenLinkCmd struct {
	Email   string `required:"" help:"대상 이메일"`
	Type    string `help:"링크 종류: unsubscribe | confirm" default:"unsubscribe" enum:"unsubscribe,confirm"`
	BaseURL string `help:"웹 프론트 URL" default:""`
	APIURL  string `help:"API 서버 URL" default:""`
}

func (c *GenLinkCmd) Run(_ context.Context, d *deps) error {
	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = d.cfg.BaseURL
	}
	apiURL := c.APIURL
	if apiURL == "" {
		apiURL = d.cfg.APIURL
	}

	token := makeHMACToken(c.Email, d.cfg.Secret)
	var link string
	switch c.Type {
	case "unsubscribe":
		link = fmt.Sprintf("%s/?action=unsubscribe&token=%s", strings.TrimSuffix(baseURL, "/"), token)
	case "confirm":
		link = fmt.Sprintf("%s/api/confirm?token=%s", strings.TrimSuffix(apiURL, "/"), token)
	}
	fmt.Println(link)
	return nil
}

func makeHMACToken(email, secret string) string {
	exp := time.Now().Add(48 * time.Hour).Unix()
	msg := fmt.Sprintf("%s:%d", email, exp)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	payload := base64.RawURLEncoding.EncodeToString([]byte(msg))
	return payload + "." + sig
}
