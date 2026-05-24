package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/maeilham/server/internal/config"
	"github.com/maeilham/server/internal/content"
	"github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/mail"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	logger := config.NewLogger(cfg.LogLevel)

	conn, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		logger.Error("db open", "err", err)
		os.Exit(1)
	}
	defer conn.Close()
	if err := db.Migrate(conn); err != nil {
		logger.Error("db migrate", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch sub {
	case "sync":
		if err := runSync(ctx, logger, conn, cfg, args); err != nil {
			logger.Error("sync failed", "err", err)
			os.Exit(1)
		}
	case "send-test":
		if err := runSendTest(ctx, logger, cfg, args); err != nil {
			logger.Error("send-test failed", "err", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func runSendTest(ctx context.Context, logger *slog.Logger, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("send-test", flag.ExitOnError)
	to := fs.String("to", "", "recipient email address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *to == "" {
		return fmt.Errorf("--to is required")
	}

	mailer := mail.New(logger, cfg.ResendAPIKey, cfg.MailFromEmail, cfg.MailFromName)
	msg := mail.Message{
		To:       *to,
		Subject:  "[매일함] 테스트 메일",
		TextBody: "이 메일이 보이면 발송 어댑터가 정상 동작 중입니다.\n— 매일함",
		HTMLBody: `<p>이 메일이 보이면 발송 어댑터가 정상 동작 중입니다.</p><p>— 매일함</p>`,
	}
	if err := mailer.Send(ctx, msg); err != nil {
		return err
	}
	logger.Info("test mail dispatched", "to", *to)
	return nil
}

func runSync(ctx context.Context, logger *slog.Logger, conn *sql.DB, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	repoSlug := fs.String("repo", "", "repo slug to sync (must exist in repos table); empty = all active")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rows, err := selectRepos(ctx, conn, *repoSlug)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		logger.Warn("no repos to sync")
		return nil
	}

	gh := content.NewGitHubClient(cfg.GitHubToken)
	for _, r := range rows {
		logger := logger.With("repo", r.slug)
		logger.Info("sync starting", "url", r.url)
		stats, err := content.Sync(ctx, logger, conn, gh, r.slug, r.url, "")
		if err != nil {
			logger.Error("sync", "err", err)
			continue
		}
		logger.Info("sync done",
			"scanned", stats.Scanned, "inserted", stats.Inserted,
			"updated", stats.Updated, "deleted", stats.Deleted,
			"skipped", stats.Skipped, "errors", stats.Errors)
	}
	return nil
}

type repoRow struct {
	slug string
	url  string
}

func selectRepos(ctx context.Context, conn *sql.DB, only string) ([]repoRow, error) {
	q := `SELECT slug, github_url FROM repos WHERE active = 1`
	args := []any{}
	if only != "" {
		q += ` AND slug = ?`
		args = append(args, only)
	}
	rows, err := conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []repoRow
	for rows.Next() {
		var r repoRow
		if err := rows.Scan(&r.slug, &r.url); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: cron <command> [flags]

commands:
  sync [--repo <slug>]   콘텐츠 repo를 fetch + DB로 sync (slug 미지정 시 모든 active repo)
  send-test --to <email> 발송 어댑터 동작 확인용 메일 1통 전송`)
}
