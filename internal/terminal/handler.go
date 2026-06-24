package terminal

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/charmbracelet/glamour"
)

//go:embed banner.txt
var bannerRaw string

var styleMap = map[string]string{
	"§r":        "\x1b[0m",
	"§bold":     "\x1b[1m",
	"§dim":      "\x1b[2m",
	"§red":      "\x1b[31m",
	"§green":    "\x1b[32m",
	"§yellow":   "\x1b[33m",
	"§blue":     "\x1b[34m",
	"§gray":     "\x1b[90m",
	"§bg-green": "\x1b[42m",
	"§bg-red":   "\x1b[41m",
}

func applyStyle(s string) string {
	for token, code := range styleMap {
		s = strings.ReplaceAll(s, token, code)
	}
	return s
}

func sprint(s string) string {
	return applyStyle(strings.ReplaceAll(s, "\n", "\r\n"))
}

type ContentItem struct {
	ContentID     string
	Title         string
	Preview       string
	Tags          []string
	GitHubURL     string // https://github.com/owner/repo
	BodyPath      string // content/0001-slug.md
	DiscussionURL string
}

func NewHandler(svc Service) SessionHandler {
	return func(rw io.ReadWriter, env map[string]string) {
		action := env["MAEILHAM_ACTION"]
		token := env["MAEILHAM_TOKEN"]
		status := env["MAEILHAM_STATUS"]

		if action == "unsubscribe" && token != "" {
			handleUnsubscribe(rw, svc, token)
			return
		}
		if status == "confirmed" {
			handleConfirmed(rw)
		}
		runREPL(rw, svc)
	}
}

func runREPL(rw io.ReadWriter, svc Service) {
	printBanner(rw)
	cmdToday(rw, svc)
	var history []string
	for {
		fmt.Fprint(rw, sprint("§green>§r "))
		line := readLine(rw, history)
		if line == "\x04" {
			return // 연결 종료
		}
		if line == "\x03" {
			fmt.Fprint(rw, "\r\n")
			return
		}
		if line == "\x03\x03" {
			continue
		}
		line = strings.TrimSpace(line)
		fmt.Fprint(rw, "\r\n")

		if line == "" {
			continue
		}
		history = append(history, line)

		parts := strings.Fields(line)
		switch parts[0] {
		case "help", "?":
			printHelp(rw)
		case "today":
			cmdToday(rw, svc)
		case "list":
			cmdList(rw, svc)
		case "show":
			cmdShow(rw, svc, parts)
		case "subscribe":
			cmdSubscribe(rw, svc, parts)
		case "exit", "quit", "q":
			fmt.Fprint(rw, "bye!\r\n\r\n")
			return
		default:
			fmt.Fprint(rw, sprint(fmt.Sprintf("§red알 수 없는 명령어: %s§r  help를 입력해보세요.\n\n", parts[0])))
		}
	}
}

func printBanner(rw io.ReadWriter) {
	out := strings.ReplaceAll(bannerRaw, "\n", "\r\n")
	fmt.Fprint(rw, applyStyle(out))
}

func printHelp(rw io.ReadWriter) {
	fmt.Fprint(rw, sprint(
		"\n"+
			"  §boldtoday§r            오늘의 질문\n"+
			"  §boldlist§r             최근 질문 목록\n"+
			"  §boldshow <id>§r        질문 본문 보기\n"+
			"  §boldsubscribe§r        이메일 구독\n"+
			"  §boldexit§r             종료\n\n",
	))
}

func cmdToday(rw io.ReadWriter, svc Service) {
	c, err := svc.TodayContent(context.Background())
	if err != nil {
		fmt.Fprint(rw, sprint(fmt.Sprintf("§red오류: %s§r\n\n", err)))
		return
	}
	if c == nil {
		fmt.Fprint(rw, sprint("§dim아직 등록된 질문이 없습니다.§r\n\n"))
		return
	}
	if c.DiscussionURL == "" {
		if url, err := svc.EnsureDiscussion(context.Background(), c.ContentID); err == nil {
			c.DiscussionURL = url
		}
	}
	renderContent(rw, c)
}

func cmdList(rw io.ReadWriter, svc Service) {
	items, err := svc.ListContents(context.Background(), 10)
	if err != nil {
		fmt.Fprint(rw, sprint(fmt.Sprintf("§red오류: %s§r\n\n", err)))
		return
	}
	if len(items) == 0 {
		fmt.Fprint(rw, sprint("§dim아직 등록된 질문이 없습니다.§r\n\n"))
		return
	}
	fmt.Fprint(rw, "\r\n")
	for _, c := range items {
		tags := ""
		if len(c.Tags) > 0 {
			tags = sprint("  §dim[" + strings.Join(c.Tags, ", ") + "]§r")
		}
		fmt.Fprint(rw, sprint(fmt.Sprintf("  §bold%s§r  %s%s\n", c.ContentID, c.Title, tags)))
	}
	fmt.Fprint(rw, "\r\n")
}

func cmdSubscribe(rw io.ReadWriter, svc Service, parts []string) {
	var email string
	if len(parts) >= 2 {
		email = strings.TrimSpace(strings.ToLower(parts[1]))
	} else {
		fmt.Fprint(rw, "이메일을 입력하세요: ")
		email = strings.TrimSpace(strings.ToLower(readLine(rw, nil)))
		fmt.Fprint(rw, "\r\n")
	}
	if email == "" {
		return
	}
	if err := svc.Subscribe(context.Background(), email); err != nil {
		fmt.Fprint(rw, sprint(fmt.Sprintf("§red오류: %s§r\n\n", err)))
		return
	}
	fmt.Fprint(rw, sprint("§green✓§r 확인 메일을 보냈습니다. 메일함을 확인해주세요.\n\n"))
}

func cmdShow(rw io.ReadWriter, svc Service, parts []string) {
	if len(parts) < 2 {
		fmt.Fprint(rw, "사용법: show <id>  (예: show 0001)\r\n\r\n")
		return
	}
	c, err := svc.GetContent(context.Background(), parts[1])
	if err != nil {
		fmt.Fprint(rw, sprint(fmt.Sprintf("§red오류: %s§r\n\n", err)))
		return
	}
	if c == nil {
		fmt.Fprint(rw, sprint(fmt.Sprintf("§red%s 를 찾을 수 없습니다.§r\n\n", parts[1])))
		return
	}
	if c.GitHubURL == "" || c.BodyPath == "" {
		renderContent(rw, c)
		return
	}
	// https://github.com/owner/repo → https://raw.githubusercontent.com/owner/repo/main/path
	rawURL := strings.Replace(c.GitHubURL, "https://github.com/", "https://raw.githubusercontent.com/", 1)
	rawURL = rawURL + "/main/" + c.BodyPath

	resp, err := http.Get(rawURL)
	if err != nil || resp.StatusCode != 200 {
		renderContent(rw, c)
		return
	}
	defer resp.Body.Close()

	var raw strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			raw.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStylePath("dark"),
		glamour.WithWordWrap(0),
	)
	if err != nil {
		renderContent(rw, c)
		return
	}
	rendered, err := renderer.Render(raw.String())
	if err != nil {
		renderContent(rw, c)
		return
	}
	// glamour는 \n 쓰므로 터미널용 \r\n 으로 변환
	out := strings.ReplaceAll(rendered, "\n", "\r\n")
	fmt.Fprint(rw, out)
}

func renderContent(rw io.ReadWriter, c *ContentItem) {
	fmt.Fprint(rw, "\r\n")
	if len(c.Tags) > 0 {
		fmt.Fprint(rw, sprint(fmt.Sprintf("  §dim[%s]§r\n", strings.Join(c.Tags, ", "))))
	}
	fmt.Fprint(rw, sprint(fmt.Sprintf("  §bold%s§r\n\n", c.Title)))
	for _, line := range strings.Split(c.Preview, "\n") {
		fmt.Fprintf(rw, "  %s\r\n", line)
	}
	if c.DiscussionURL != "" {
		fmt.Fprint(rw, sprint(fmt.Sprintf("\n  §dim→ %s§r\n", c.DiscussionURL)))
	}
	fmt.Fprint(rw, "\r\n")
}

func handleConfirmed(rw io.ReadWriter) {
	fmt.Fprint(rw, sprint(
		"\n"+
			"§green✓ 구독이 완료됐습니다!§r\n\n"+
			"§dim  내일부터 매일 아침 질문이 도착합니다.§r\n\n",
	))
}

func handleUnsubscribe(rw io.ReadWriter, svc Service, token string) {
	fmt.Fprint(rw, "\r\n구독을 취소하시겠어요? (y/n): ")
	line := readLine(rw, nil)
	fmt.Fprintf(rw, "\r\n")
	if strings.TrimSpace(strings.ToLower(line)) != "y" {
		fmt.Fprint(rw, "취소하지 않았습니다.\r\n\r\n")
		return
	}
	if err := svc.Unsubscribe(context.Background(), token); err != nil {
		fmt.Fprint(rw, sprint(fmt.Sprintf("§red오류: %s§r\n", err)))
		return
	}
	fmt.Fprint(rw, sprint("§green✓§r 구독이 취소됐습니다.\n\n"))
}

func replaceLineBuf(rw io.ReadWriter, buf *[]byte, line string) {
	for range *buf {
		fmt.Fprint(rw, "\b \b")
	}
	fmt.Fprint(rw, line)
	*buf = []byte(line)
}

func readLine(rw io.ReadWriter, history []string) string {
	var buf []byte
	b := make([]byte, 1)
	histIdx := len(history)
	saved := ""
	for {
		n, err := rw.Read(b)
		if err != nil {
			return "\x04" // 연결 종료
		}
		if n == 0 {
			continue
		}
		ch := b[0]
		if ch == '\r' || ch == '\n' {
			return string(buf)
		}
		if ch == 3 { // Ctrl+C
			if len(buf) == 0 {
				return "\x03"
			}
			fmt.Fprint(rw, "^C\r\n")
			return "\x03\x03"
		}
		if ch == '\x1b' { // 이스케이프 시퀀스
			seq := make([]byte, 2)
			rw.Read(seq)
			if seq[0] == '[' {
				switch seq[1] {
				case 'A': // Up
					if histIdx > 0 {
						if histIdx == len(history) {
							saved = string(buf)
						}
						histIdx--
						replaceLineBuf(rw, &buf, history[histIdx])
					}
				case 'B': // Down
					if histIdx < len(history) {
						histIdx++
						line := saved
						if histIdx < len(history) {
							line = history[histIdx]
						}
						replaceLineBuf(rw, &buf, line)
					}
				}
			}
			continue
		}
		if ch == 127 || ch == 8 {
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				fmt.Fprint(rw, "\b \b")
			}
			continue
		}
		buf = append(buf, ch)
		fmt.Fprint(rw, string(ch))
	}
}
