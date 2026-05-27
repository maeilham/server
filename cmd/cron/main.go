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

	"time"

	"github.com/maeilham/server/internal/content"
	"github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/delivery"
	"github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/pkg/closeutil"
	"github.com/maeilham/server/internal/pkg/config"
	"github.com/maeilham/server/internal/pkg/logger"
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
	logger := logger.New(cfg.LogLevel)

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
	case "send-daily":
		if err := runSendDaily(ctx, logger, conn, cfg, args); err != nil {
			logger.Error("send-daily failed", "err", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func runSendDaily(ctx context.Context, logger *slog.Logger, conn *sql.DB, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("send-daily", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "결정만 로그하고 실제 발송/DB 변경은 하지 않음")
	dateStr := fs.String("date", "", "기준 날짜 YYYY-MM-DD (미지정 시 오늘)")
	baseURL := fs.String("base-url", "https://maeilham.kr", "사이트 base URL (unsubscribe 링크 생성)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	day := time.Now().UTC()
	if *dateStr != "" {
		d, err := time.Parse("2006-01-02", *dateStr)
		if err != nil {
			return fmt.Errorf("invalid --date: %w", err)
		}
		day = d
	}

	mailer := mail.New(logger, cfg.ResendAPIKey, cfg.MailFromEmail, cfg.MailFromName)
	stats, err := delivery.DailySend(ctx, logger, conn, mailer, delivery.DailySendOptions{
		Day:     day,
		DryRun:  *dryRun,
		BaseURL: *baseURL,
	})
	if err != nil {
		return err
	}
	logger.Info("send-daily done",
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
	subject, text, html := mail.RenderDaily(mail.DailyMailData{
		RepoName:       "백엔드 면접",
		Title:          "인덱스(Index)가 무엇이고, 어떻게 동작하나요?",
		Preview:        "DB 인덱스는 데이터 검색 속도를 높이기 위해 별도로 관리하는 자료구조입니다. B-Tree 구조로 O(log n) 탐색을 지원하며, SELECT 성능을 크게 향상시킬 수 있습니다.",
		GitHubURL:      "https://github.com/maeilham/be-interview/blob/main/content/0001-index.md",
		DiscussionURL:  "https://github.com/maeilham/be-interview/discussions/1",
		UnsubscribeURL: "https://maeilham.kr/unsubscribe?sid=0",
	})
	msg := mail.Message{
		To:       *to,
		Subject:  subject,
		TextBody: text,
		HTMLBody: html,
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
	defer closeutil.Discard(rows)
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
  sync [--repo <slug>]        콘텐츠 repo를 fetch + DB로 sync (slug 미지정 시 모든 active repo)
  send-test --to <email>      발송 어댑터 동작 확인용 메일 1통 전송
  send-daily [--dry-run] [--date YYYY-MM-DD] [--base-url <url>]
                               활성 구독자 전체에 오늘의 메일 발송`)
}
