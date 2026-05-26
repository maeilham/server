package mail

import (
	"fmt"
	"strings"
)

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
}

// RenderDaily produces (subject, text). HTML rendering is deferred to Phase 1.6;
// for now Resend can send the text body alone, which is enough for the maintainer
// to start receiving daily mail end-to-end.
func RenderDaily(d DailyMailData) (subject, text string) {
	subject = fmt.Sprintf("[매일함] %s", d.Title)

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
	return subject, b.String()
}
