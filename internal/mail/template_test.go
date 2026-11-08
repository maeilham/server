package mail

import (
	"strings"
	"testing"
)

func TestRenderLink(t *testing.T) {
	const link = "https://web.example/#t=abc123"
	subject, text, html := RenderLink(link)

	if subject == "" {
		t.Error("subject is empty")
	}
	if !strings.Contains(text, link) {
		t.Errorf("text body has no link:\n%s", text)
	}
	// html/template이 href의 '#'와 '='를 망가뜨리지 않는지(토큰은 # 뒤에 있다)
	if want := `href="` + link + `"`; !strings.Contains(html, want) {
		t.Errorf("html body does not contain %s", want)
	}
	// 이 메일은 만료되지 않는 개인 링크라서 옛 확인 메일의 만료 문구가 남으면 안 된다
	for name, body := range map[string]string{"text": text, "html": html} {
		if strings.Contains(body, "48시간") {
			t.Errorf("%s body still mentions the 48h expiry", name)
		}
		if !strings.Contains(body, "공유하지 마세요") {
			t.Errorf("%s body has no warning against sharing the link", name)
		}
	}
}

func TestRenderLink_EscapesUnsafeURL(t *testing.T) {
	_, _, html := RenderLink(`https://web.example/#t="><script>alert(1)</script>`)
	if strings.Contains(html, "<script>") {
		t.Error("link URL was injected into the html unescaped")
	}
}

func TestRenderRotatedLink(t *testing.T) {
	const link = "https://web.example/#t=abc123"
	subject, text, html := RenderRotatedLink(link)

	if subject == "" {
		t.Error("subject is empty")
	}
	if !strings.Contains(text, link) {
		t.Errorf("text body has no link:\n%s", text)
	}
	if want := `href="` + link + `"`; !strings.Contains(html, want) {
		t.Errorf("html body does not contain %s", want)
	}
	// 가입 메일 문구가 남으면 기존 구독자가 "처음 가입"으로 오해한다
	for name, body := range map[string]string{"text": text, "html": html} {
		if strings.Contains(body, "환영") || strings.Contains(body, "가입이 완료") {
			t.Errorf("%s body still has signup wording", name)
		}
		if !strings.Contains(body, "이전 링크는 이제 쓸 수 없") {
			t.Errorf("%s body does not say the old link stopped working", name)
		}
		if !strings.Contains(body, "공유하지 마세요") {
			t.Errorf("%s body has no warning against sharing the link", name)
		}
	}
}

func TestRenderRotatedLink_EscapesUnsafeURL(t *testing.T) {
	_, _, html := RenderRotatedLink(`https://web.example/#t="><script>alert(1)</script>`)
	if strings.Contains(html, "<script>") {
		t.Error("link URL was injected into the html unescaped")
	}
}

func TestRenderNewQuestions(t *testing.T) {
	d := NewQuestionMailData{
		Questions:      []NewQuestion{{RepoName: "백엔드/운영", Title: "SYN flooding이란?", Preview: "서버가 어떻게 되나요?"}},
		WebURL:         "https://web.example/",
		UnsubscribeURL: "https://web.example/?action=unsubscribe&token=t",
	}
	subject, text, html := RenderNewQuestions(d)

	if !strings.Contains(subject, "SYN flooding이란?") {
		t.Errorf("subject has no title: %q", subject)
	}
	for name, body := range map[string]string{"text": text, "html": html} {
		for _, want := range []string{"새로운 질문이 생겼어요", "SYN flooding이란?", "https://web.example/"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s body missing %q", name, want)
			}
		}
	}
	if want := `href="https://web.example/"`; !strings.Contains(html, want) {
		t.Errorf("html has no CTA link %s", want)
	}
}

func TestRenderNewQuestions_SeveralQuestions(t *testing.T) {
	subject, text, _ := RenderNewQuestions(NewQuestionMailData{
		Questions: []NewQuestion{{RepoName: "a", Title: "첫째"}, {RepoName: "b", Title: "둘째"}},
		WebURL:    "https://web.example/",
	})
	if !strings.Contains(subject, "2개") {
		t.Errorf("subject = %q, want count", subject)
	}
	if !strings.Contains(text, "첫째") || !strings.Contains(text, "둘째") {
		t.Errorf("text lacks a question:\n%s", text)
	}
}

func TestRenderNewQuestions_EscapesUnsafeInput(t *testing.T) {
	_, _, html := RenderNewQuestions(NewQuestionMailData{
		Questions: []NewQuestion{{RepoName: "r", Title: "<script>alert(1)</script>", Preview: "<img src=x onerror=alert(1)>"}},
		WebURL:    `https://web.example/"><script>alert(2)</script>`,
	})
	if strings.Contains(html, "<script>") || strings.Contains(html, "<img src=x") {
		t.Error("input was injected into the html unescaped")
	}
}
