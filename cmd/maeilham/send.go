package main

import (
	"context"
	"fmt"
	"time"

	"github.com/maeilham/server/internal/delivery"
	"github.com/maeilham/server/internal/mail"
)

type NotifyCmd struct {
	DryRun  bool   `help:"결정만 로그하고 메일 발송과 발송 기록은 하지 않음 (오늘의 질문은 정해져 저장될 수 있음)"`
	Date    string `help:"기준 날짜 YYYY-MM-DD, MAEILHAM_TZ 시간대의 그날 (미지정 시 오늘)"`
	BaseURL string `help:"사이트 base URL (미지정 시 환경변수)"`
}

func (c *NotifyCmd) Run(ctx context.Context, d *deps) error {
	loc, err := d.location()
	if err != nil {
		return err
	}
	day, err := sendDay(time.Now(), c.Date, loc)
	if err != nil {
		return err
	}
	return notifyNewQuestions(ctx, d, loc, day, c.DryRun, c.BaseURL)
}

// notifyNewQuestions는 day(서비스 시간대의 하루)에 처음 나오는 새 질문을 구독자에게 알린다. CLI와 스케줄러가 같이 쓴다.
func notifyNewQuestions(ctx context.Context, d *deps, loc *time.Location, day time.Time, dryRun bool, baseURL string) error {
	if baseURL == "" {
		baseURL = d.cfg.BaseURL
	}
	stats, err := delivery.NotifyNewQuestions(ctx, d.log, d.mailer(), delivery.NotifyOptions{
		Day:           day,
		Loc:           loc,
		DryRun:        dryRun,
		BaseURL:       baseURL,
		Secret:        d.cfg.Secret,
		GitHubApp:     d.ghApp(),
		SubRepo:       d.subRepo,
		RepoStore:     d.repoStore,
		ContentStore:  d.contentStore,
		LogStore:      d.logStore,
		QuestionStore: d.questionStore,
	})
	if err != nil {
		return err
	}
	if stats.NewQuestions > 0 || stats.Errors > 0 {
		d.log.Info("notify done",
			"dry_run", stats.DryRun,
			"new_questions", stats.NewQuestions,
			"subscribers", stats.Subscribers,
			"sent", stats.Sent,
			"skipped", stats.Skipped,
			"errors", stats.Errors)
	}
	return nil
}

// sendDay는 발송 기준 시각을 정한다. --date가 없으면 지금이고, 있으면 서비스 시간대(loc)에서 그날 0시다.
// (이전에는 UTC 0시로 해석해서 한국 운영자가 생각하는 날짜와 9시간 어긋났다)
func sendDay(now time.Time, date string, loc *time.Location) (time.Time, error) {
	if date == "" {
		return now, nil
	}
	t, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --date: %w", err)
	}
	return t, nil
}

type SendTestCmd struct {
	To string `required:"" help:"수신 이메일 주소"`
}

func (c *SendTestCmd) Run(ctx context.Context, d *deps) error {
	subject, text, html := mail.RenderNewQuestions(mail.NewQuestionMailData{
		Questions: []mail.NewQuestion{{
			RepoName: "백엔드 면접",
			Title:    "인덱스(Index)가 무엇이고, 어떻게 동작하나요?",
			Preview:  "DB 인덱스는 데이터 검색 속도를 높이기 위해 별도로 관리하는 자료구조입니다.",
		}},
		WebURL:         "https://maeilham.github.io/",
		UnsubscribeURL: "https://maeilham.github.io/?action=unsubscribe&token=test",
	})
	if err := d.mailer().Send(ctx, mail.Message{
		To: c.To, Subject: subject, TextBody: text, HTMLBody: html,
	}); err != nil {
		return err
	}
	d.log.Info("test mail dispatched", "to", c.To)
	return nil
}
