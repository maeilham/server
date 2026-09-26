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
