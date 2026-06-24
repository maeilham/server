package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/maeilham/server/internal/content"
	"github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/delivery"
	gh "github.com/maeilham/server/internal/github"
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
	case "summarize":
		if err := runSummarize(ctx, logger, conn, cfg, args); err != nil {
			logger.Error("summarize failed", "err", err)
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
	case "gen-link":
		if err := runGenLink(cfg, args); err != nil {
			logger.Error("gen-link failed", "err", err)
			os.Exit(1)
		}
	case "repo":
		if err := runRepo(ctx, conn, args); err != nil {
			logger.Error("repo failed", "err", err)
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
	baseURL := fs.String("base-url", cfg.BaseURL, "사이트 base URL (unsubscribe 링크 생성)")
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

	var ghApp *gh.App
	if cfg.GitHubAppID != 0 && cfg.GitHubAppPemPath != "" && cfg.GitHubInstallationID != 0 {
		if app, appErr := gh.NewApp(cfg.GitHubAppID, cfg.GitHubInstallationID, cfg.GitHubAppPemPath); appErr != nil {
			logger.Warn("github app init failed (discussion 생성 비활성화)", "err", appErr)
		} else {
			ghApp = app
		}
	}

	stats, err := delivery.DailySend(ctx, logger, conn, mailer, delivery.DailySendOptions{
		Day:       day,
		DryRun:    *dryRun,
		BaseURL:   *baseURL,
		APIURL:    cfg.APIURL,
		Secret:    cfg.Secret,
		GitHubApp: ghApp,
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

	ghClient := content.NewGitHubClient(cfg.GitHubToken)

	var ghApp *gh.App
	if cfg.GitHubAppID != 0 && cfg.GitHubAppPemPath != "" && cfg.GitHubInstallationID != 0 {
		if app, appErr := gh.NewApp(cfg.GitHubAppID, cfg.GitHubInstallationID, cfg.GitHubAppPemPath); appErr != nil {
			logger.Warn("github app init failed (discussion 업데이트 비활성화)", "err", appErr)
		} else {
			ghApp = app
		}
	}

	for _, r := range rows {
		logger := logger.With("repo", r.slug)
		logger.Info("sync starting", "url", r.url)
		stats, err := content.Sync(ctx, logger, conn, ghClient, ghApp, r.slug, r.url, "")
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

func runGenLink(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("gen-link", flag.ExitOnError)
	email := fs.String("email", "", "대상 이메일")
	kind := fs.String("type", "unsubscribe", "링크 종류: unsubscribe | confirm")
	baseURL := fs.String("base-url", cfg.BaseURL, "웹 프론트 URL")
	apiURL := fs.String("api-url", cfg.APIURL, "API 서버 URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return fmt.Errorf("--email is required")
	}

	token := makeHMACToken(*email, cfg.Secret)

	var link string
	switch *kind {
	case "unsubscribe":
		link = fmt.Sprintf("%s/?action=unsubscribe&token=%s", strings.TrimSuffix(*baseURL, "/"), token)
	case "confirm":
		link = fmt.Sprintf("%s/api/confirm?token=%s", strings.TrimSuffix(*apiURL, "/"), token)
	default:
		return fmt.Errorf("unknown type: %s", *kind)
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

func runRepo(ctx context.Context, conn *sql.DB, args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cron repo <add|list|deactivate>")
		return fmt.Errorf("subcommand required")
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "add":
		fs := flag.NewFlagSet("repo add", flag.ExitOnError)
		slug := fs.String("slug", "", "레포 슬러그 (예: backend-interview)")
		url := fs.String("url", "", "GitHub URL (예: https://github.com/maeilham/backend-interview)")
		name := fs.String("name", "", "표시 이름")
		desc := fs.String("desc", "", "설명 (선택)")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *slug == "" || *url == "" || *name == "" {
			return fmt.Errorf("--slug, --url, --name 은 필수입니다")
		}
		_, err := conn.ExecContext(ctx,
			`INSERT INTO repos (slug, github_url, display_name, description, active)
			 VALUES (?, ?, ?, ?, 1)
			 ON CONFLICT(slug) DO UPDATE SET
			   github_url = excluded.github_url,
			   display_name = excluded.display_name,
			   description = excluded.description,
			   active = 1`,
			*slug, *url, *name, *desc,
		)
		if err != nil {
			return fmt.Errorf("insert repo: %w", err)
		}
		fmt.Printf("✓ repo 추가됨: %s\n", *slug)
		return nil

	case "list":
		rows, err := conn.QueryContext(ctx,
			`SELECT slug, display_name, github_url, active FROM repos ORDER BY slug`)
		if err != nil {
			return err
		}
		defer rows.Close()
		fmt.Printf("%-25s %-10s %s\n", "SLUG", "ACTIVE", "URL")
		fmt.Println(strings.Repeat("-", 70))
		for rows.Next() {
			var slug, name, url string
			var active int
			if err := rows.Scan(&slug, &name, &url, &active); err != nil {
				return err
			}
			status := "✓"
			if active == 0 {
				status = "✗"
			}
			fmt.Printf("%-25s %-10s %s  (%s)\n", slug, status, url, name)
		}
		return rows.Err()

	case "deactivate":
		fs := flag.NewFlagSet("repo deactivate", flag.ExitOnError)
		slug := fs.String("slug", "", "레포 슬러그")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *slug == "" {
			return fmt.Errorf("--slug 은 필수입니다")
		}
		_, err := conn.ExecContext(ctx, `UPDATE repos SET active = 0 WHERE slug = ?`, *slug)
		if err != nil {
			return err
		}
		fmt.Printf("✓ repo 비활성화됨: %s\n", *slug)
		return nil

	default:
		return fmt.Errorf("알 수 없는 서브커맨드: %s (add|list|deactivate)", sub)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: cron <command> [flags]

commands:
  repo add --slug <slug> --url <github_url> --name <name>
                               레포 등록/수정
  repo list                    등록된 레포 목록
  repo deactivate --slug <slug>
                               레포 비활성화
  sync [--repo <slug>]         콘텐츠 repo를 fetch + DB로 sync
  summarize --repo <slug> --content-id <id>
                               Discussion 댓글을 AI로 요약해 본문 업데이트
  send-test --to <email>       발송 어댑터 동작 확인용 메일 1통 전송
  send-daily [--dry-run] [--date YYYY-MM-DD]
                               활성 구독자 전체에 오늘의 메일 발송
  gen-link --email <email> [--type unsubscribe|confirm]
                               테스트용 링크 생성`)
}

func runSummarize(ctx context.Context, logger *slog.Logger, conn *sql.DB, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("summarize", flag.ExitOnError)
	repoSlug := fs.String("repo", "", "레포 슬러그")
	contentID := fs.String("content-id", "", "콘텐츠 ID")
	aiCmd := fs.String("ai-cmd", "claude --print", "AI CLI 커맨드 (stdin으로 프롬프트 주입, stdout으로 결과 수신)")
	yes := fs.Bool("yes", false, "확인 없이 바로 업데이트")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repoSlug == "" || *contentID == "" {
		return fmt.Errorf("--repo 와 --content-id 는 필수입니다")
	}

	// 1. DB에서 github_url, discussion_url, body_path 조회
	var githubURL, bodyPath string
	var discussionURL sql.NullString
	err := conn.QueryRowContext(ctx, `
		SELECT r.github_url, c.discussion_url, c.body_path
		  FROM contents c
		  JOIN repos r ON r.slug = c.repo_slug
		 WHERE c.repo_slug = ? AND c.content_id = ? AND c.deleted_at IS NULL`,
		*repoSlug, *contentID,
	).Scan(&githubURL, &discussionURL, &bodyPath)
	if err == sql.ErrNoRows {
		return fmt.Errorf("콘텐츠를 찾을 수 없습니다: %s/%s", *repoSlug, *contentID)
	}
	if err != nil {
		return fmt.Errorf("query content: %w", err)
	}
	if !discussionURL.Valid || discussionURL.String == "" {
		return fmt.Errorf("discussion URL이 없습니다 (content_id: %s)", *contentID)
	}

	// 2. GitHub URL 파싱
	owner, repo, err := content.ParseGitHubURL(githubURL)
	if err != nil {
		return fmt.Errorf("parse github URL: %w", err)
	}

	// 3. Discussion 번호 파싱
	number, err := parseDiscussionNumber(discussionURL.String)
	if err != nil {
		return fmt.Errorf("parse discussion URL: %w", err)
	}

	// 4. summary-config.md + content 파일 로드
	ghClient := content.NewGitHubClient(cfg.GitHubToken)
	configBytes, err := ghClient.FetchRaw(ctx, owner, repo, "main", "content/summary-config.md")
	if err != nil {
		return fmt.Errorf("summary-config.md fetch 실패: %w", err)
	}
	systemPrompt := string(configBytes)

	contentBytes, err := ghClient.FetchRaw(ctx, owner, repo, "main", bodyPath)
	if err != nil {
		return fmt.Errorf("content 파일 fetch 실패: %w", err)
	}

	// 5. Discussion 댓글 fetch
	ghApp, err := gh.NewApp(cfg.GitHubAppID, cfg.GitHubInstallationID, cfg.GitHubAppPemPath)
	if err != nil {
		return fmt.Errorf("github app init 실패: %w", err)
	}
	discussion, err := ghApp.FetchDiscussion(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("discussion fetch 실패: %w", err)
	}
	if len(discussion.Comments) == 0 {
		logger.Info("댓글 없음, 스킵", "content_id", *contentID)
		return nil
	}

	// 6. 댓글을 텍스트로 조합
	var commentBuf strings.Builder
	for i, c := range discussion.Comments {
		fmt.Fprintf(&commentBuf, "### 댓글 %d (%s)\n%s\n\n", i+1, c.Author, c.Body)
	}

	// 7. AI CLI 실행
	logger.Info("AI 요약 중...", "comments", len(discussion.Comments), "cmd", *aiCmd)
	prompt := systemPrompt + "\n\n---\n\n## 오늘의 주제\n\n" + string(contentBytes) + "\n\n---\n\n## 댓글 목록\n\n" + commentBuf.String()
	cmd := exec.CommandContext(ctx, "sh", "-c", *aiCmd)
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("AI 실행 실패: %w", err)
	}
	summary := strings.TrimSpace(string(out))

	// 8. 사용자에게 결과 출력 후 확인
	fmt.Println("\n" + strings.Repeat("━", 60))
	fmt.Println(summary)
	fmt.Println(strings.Repeat("━", 60))

	if !*yes {
		fmt.Print("\nDiscussion 본문을 업데이트할까요? (y/N): ")
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if answer != "y" {
			fmt.Println("취소됨.")
			return nil
		}
	}

	// 9. Discussion 본문 업데이트
	newBody := buildDiscussionBody(discussion.Body, summary)
	if err := ghApp.UpdateDiscussionBody(ctx, owner, repo, discussion.NodeID, newBody); err != nil {
		return fmt.Errorf("discussion 업데이트 실패: %w", err)
	}
	logger.Info("summarize 완료", "content_id", *contentID, "comments", len(discussion.Comments))
	return nil
}

func parseDiscussionNumber(discussionURL string) (int, error) {
	parts := strings.Split(discussionURL, "/discussions/")
	if len(parts) != 2 || parts[1] == "" {
		return 0, fmt.Errorf("invalid discussion URL: %s", discussionURL)
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, fmt.Errorf("invalid discussion number in URL: %s", discussionURL)
	}
	return n, nil
}

func buildDiscussionBody(currentBody, summary string) string {
	const (
		markerStart = "<!-- maeilham-summary-start -->"
		markerEnd   = "<!-- maeilham-summary-end -->"
	)
	section := markerStart + "\n## 💬 코멘트 요약\n*AI가 정리한 내용입니다*\n\n" + summary + "\n" + markerEnd

	if before, rest, found := strings.Cut(currentBody, markerStart); found {
		_, after, _ := strings.Cut(rest, markerEnd)
		return strings.TrimRight(before, "\n") + "\n\n" + section + after
	}
	return currentBody + "\n\n" + section
}
