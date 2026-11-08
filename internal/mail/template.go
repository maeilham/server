package mail

import (
	_ "embed"
	"bytes"
	"fmt"
	"html/template"
	"strings"
)

//go:embed new_question.html
var newQuestionHTMLTmpl string

//go:embed confirm.html
var confirmHTMLTmpl string

//go:embed link.html
var linkHTMLTmpl string

//go:embed link_rotated.html
var linkRotatedHTMLTmpl string

var newQuestionTmpl = template.Must(template.New("new_question").Parse(newQuestionHTMLTmpl))
var confirmTmpl = template.Must(template.New("confirm").Parse(confirmHTMLTmpl))
var linkTmpl    = template.Must(template.New("link").Parse(linkHTMLTmpl))
var linkRotatedTmpl = template.Must(template.New("link_rotated").Parse(linkRotatedHTMLTmpl))

// NewQuestion은 알림 메일에 담기는 새 질문 하나다. 답은 담지 않는다.
type NewQuestion struct {
	RepoName string
	Title    string
	Preview  string
}

// NewQuestionMailData carries everything the new-question mail template needs.
// The template package itself stays decoupled from delivery/content packages.
type NewQuestionMailData struct {
	Questions      []NewQuestion
	WebURL         string // 웹의 오늘의 질문 주소. 개인 링크 토큰은 싣지 않는다
	UnsubscribeURL string

	// Subject is injected into the HTML template; set by RenderNewQuestions.
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

// RenderLink produces (subject, text, html) for the personal-link email sent when someone signs up.
// 이 메일 하나가 가입 확인과 개인 링크를 겸한다. 링크를 처음 여는 것이 이메일 인증이다.
func RenderLink(linkURL string) (subject, text, html string) {
	subject = "[매일함] 나만의 링크가 도착했어요"
	text = fmt.Sprintf(`매일함에 오신 걸 환영해요.

앞으로 아래 링크로 매일함을 이용해보세요. 이 링크로 들어오면 푼 문제 기록이 저장돼요.
링크를 처음 열면 가입이 완료됩니다.

%s

- 즐겨찾기(북마크)에 넣어두면 편해요.
- 이 링크는 나만의 열쇠예요. 다른 사람에게 공유하지 마세요.
- 새 질문이 올라오는 날 아침에 메일로 알려드릴게요.

이 메일을 요청하지 않으셨다면 무시해주세요.
`, linkURL)

	var buf bytes.Buffer
	if err := linkTmpl.Execute(&buf, struct{ LinkURL string }{linkURL}); err != nil {
		panic("mail: link template execute: " + err.Error())
	}
	html = buf.String()
	return
}

// RenderRotatedLink produces (subject, text, html) for the email sent when a subscriber regenerates their personal link.
// 새 링크는 응답이 아니라 이 메일로만 전달된다(링크가 유출돼 누가 재발급을 눌러도 새 링크는 주인만 받는다).
func RenderRotatedLink(linkURL string) (subject, text, html string) {
	subject = "[매일함] 새 링크가 도착했어요"
	text = fmt.Sprintf(`'나만의 링크 새로 받기'가 요청돼서 새 링크를 보내요.
이전 링크는 이제 쓸 수 없어요. 아래 링크로 다시 들어와 주세요.

%s

- 북마크와 홈 화면 바로가기는 이 새 링크로 바꿔주세요.
- 이 링크는 나만의 열쇠예요. 다른 사람에게 공유하지 마세요.

직접 요청하신 게 아니라면 이전 링크가 다른 사람에게 알려졌을 수 있어요.
이전 링크는 이제 쓸 수 없으니 걱정하지 않으셔도 돼요.
`, linkURL)

	var buf bytes.Buffer
	if err := linkRotatedTmpl.Execute(&buf, struct{ LinkURL string }{linkURL}); err != nil {
		panic("mail: link_rotated template execute: " + err.Error())
	}
	html = buf.String()
	return
}

// RenderNewQuestions produces (subject, text, html) for the mail sent on the day new questions become today's question.
func RenderNewQuestions(d NewQuestionMailData) (subject, text, html string) {
	if len(d.Questions) == 1 {
		subject = fmt.Sprintf("[매일함] 새로운 질문이 생겼어요: %s", d.Questions[0].Title)
	} else {
		subject = fmt.Sprintf("[매일함] 새로운 질문 %d개가 생겼어요", len(d.Questions))
	}
	d.Subject = subject

	var b strings.Builder
	b.WriteString("새로운 질문이 생겼어요\n\n")
	for _, q := range d.Questions {
		fmt.Fprintf(&b, "%s\n%s\n\n", q.RepoName, q.Title)
		if q.Preview != "" {
			fmt.Fprintf(&b, "%s\n\n", q.Preview)
		}
	}
	fmt.Fprintf(&b, "지금 확인하기 → %s\n", d.WebURL)
	if d.UnsubscribeURL != "" {
		fmt.Fprintf(&b, "\n—\n구독 해지 → %s\n", d.UnsubscribeURL)
	}
	text = b.String()

	var buf bytes.Buffer
	if err := newQuestionTmpl.Execute(&buf, d); err != nil {
		// template parse errors are caught at init; Execute only fails on
		// broken Writer, which bytes.Buffer never does.
		panic("mail: new_question template execute: " + err.Error())
	}
	html = buf.String()
	return
}
