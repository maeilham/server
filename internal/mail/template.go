package mail

import (
	_ "embed"
	"bytes"
	"fmt"
	"html/template"
	"strings"
)

//go:embed daily.html
var dailyHTMLTmpl string

//go:embed confirm.html
var confirmHTMLTmpl string

var dailyTmpl   = template.Must(template.New("daily").Parse(dailyHTMLTmpl))
var confirmTmpl = template.Must(template.New("confirm").Parse(confirmHTMLTmpl))

// DailyMailData carries everything a mail template needs.
// The template package itself stays decoupled from delivery/content packages.
type DailyMailData struct {
	RepoSlug       string
	RepoName       string
	Title          string
	Preview        string
	GitHubURL      string // e.g. https://github.com/maeilham/be/blob/main/content/0001-...md
	DiscussionURL  string // optional, falls back to repo discussions index
	UnsubscribeURL string

	// Subject is injected into the HTML template; set by RenderDaily.
	Subject string
}

// RenderConfirm produces (subject, text, html) for the confirmation email.
func RenderConfirm(confirmURL string) (subject, text, html string) {
	subject = "[매일함] 구독을 확인해주세요"
	text = fmt.Sprintf("매일함 구독 확인 링크: %s\n\n48시간 후 만료됩니다.", confirmURL)

	var buf bytes.Buffer
	if err := confirmTmpl.Execute(&buf, struct{ ConfirmURL string }{confirmURL}); err != nil {
		panic("mail: confirm template execute: " + err.Error())
	}
	html = buf.String()
	return
}

// RenderDaily produces (subject, text, html).
func RenderDaily(d DailyMailData) (subject, text, html string) {
	subject = fmt.Sprintf("[매일함] %s", d.Title)
	d.Subject = subject

	var b strings.Builder
	fmt.Fprintf(&b, "오늘의 질문 (%s)\n\n", d.RepoName)
	fmt.Fprintf(&b, "%s\n\n", d.Title)
	if d.Preview != "" {
		fmt.Fprintf(&b, "%s\n\n", d.Preview)
	}
	fmt.Fprintf(&b, "전체 보기 → %s\n", d.GitHubURL)
	if d.DiscussionURL != "" {
		fmt.Fprintf(&b, "토론에 참여 → %s\n", d.DiscussionURL)
	}
	if d.UnsubscribeURL != "" {
		fmt.Fprintf(&b, "\n—\n구독 해지 → %s\n", d.UnsubscribeURL)
	}
	text = b.String()

	var buf bytes.Buffer
	if err := dailyTmpl.Execute(&buf, d); err != nil {
		// template parse errors are caught at init; Execute only fails on
		// broken Writer, which bytes.Buffer never does.
		panic("mail: daily template execute: " + err.Error())
	}
	html = buf.String()
	return
}
