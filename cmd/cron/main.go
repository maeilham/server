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
	default:
		usage()
		os.Exit(2)
	}
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
  sync [--repo <slug>]   콘텐츠 repo를 fetch + DB로 sync (slug 미지정 시 모든 active repo)`)
}
