package main

import (
	"context"
	"fmt"
	"time"

	"github.com/maeilham/server/internal/delivery"
	"github.com/maeilham/server/internal/mail"
)

type SendDailyCmd struct {
	DryRun  bool   `help:"결정만 로그하고 실제 발송/DB 변경은 하지 않음"`
	Date    string `help:"기준 날짜 YYYY-MM-DD (미지정 시 오늘)"`
	BaseURL string `help:"사이트 base URL (미지정 시 환경변수)"`
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
