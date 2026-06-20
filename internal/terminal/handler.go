package terminal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/charmbracelet/glamour"
)

type ContentItem struct {
	ContentID string
	Title     string
	Preview   string
	Tags      []string
	GitHubURL string // https://github.com/owner/repo
	BodyPath  string // content/0001-slug.md
}

type Deps struct {
	Subscribe    func(ctx context.Context, email string) error
	Unsubscribe  func(ctx context.Context, token string) error
	TodayContent func(ctx context.Context) (*ContentItem, error)
	ListContents func(ctx context.Context, limit int) ([]*ContentItem, error)
	GetContent   func(ctx context.Context, contentID string) (*ContentItem, error)
}

func NewHandler(deps Deps) SessionHandler {
	return func(rw io.ReadWriter, env map[string]string) {
		action := env["MAEILHAM_ACTION"]
		token := env["MAEILHAM_TOKEN"]
		status := env["MAEILHAM_STATUS"]

		if action == "unsubscribe" && token != "" {
			handleUnsubscribe(rw, deps, token)
			return
		}
		if status == "confirmed" {
			handleConfirmed(rw)
		}
		runREPL(rw, deps)
	}
}

func runREPL(rw io.ReadWriter, deps Deps) {
	printBanner(rw)
	for {
		fmt.Fprint(rw, "\x1b[32m>\x1b[0m ")
		line := strings.TrimSpace(readLine(rw))
		fmt.Fprint(rw, "\r\n")

		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		switch parts[0] {
		case "help", "?":
			printHelp(rw)
		case "today":
			cmdToday(rw, deps)
		case "list":
			cmdList(rw, deps)
		case "show":
			cmdShow(rw, deps, parts)
		case "subscribe":
			cmdSubscribe(rw, deps, parts)
		case "exit", "quit", "q":
			fmt.Fprint(rw, "bye!\r\n\r\n")
			return
		default:
			fmt.Fprintf(rw, "\x1b[31m알 수 없는 명령어: %s\x1b[0m  help를 입력해보세요.\r\n\r\n", parts[0])
		}
	}
}

func printBanner(rw io.ReadWriter) {
	fmt.Fprint(rw,
		"\r\n"+
			"\x1b[32m  매일함\x1b[0m\r\n"+
			"\x1b[2m  매일 아침, 질문 하나가 도착합니다.\x1b[0m\r\n\r\n"+
			"  \x1b[2mhelp\x1b[0m을 입력하면 사용할 수 있는 명령어를 볼 수 있어요.\r\n\r\n",
	)
}

func printHelp(rw io.ReadWriter) {
	fmt.Fprint(rw,
		"\r\n"+
			"  \x1b[1mtoday\x1b[0m            오늘의 질문\r\n"+
			"  \x1b[1mlist\x1b[0m             최근 질문 목록\r\n"+
			"  \x1b[1mshow <id>\x1b[0m        질문 본문 보기\r\n"+
			"  \x1b[1msubscribe\x1b[0m        이메일 구독\r\n"+
			"  \x1b[1mexit\x1b[0m             종료\r\n\r\n",
	)
}

func cmdToday(rw io.ReadWriter, deps Deps) {
	if deps.TodayContent == nil {
		fmt.Fprint(rw, "\x1b[31m콘텐츠 조회 기능이 설정되지 않았습니다.\x1b[0m\r\n\r\n")
		return
	}
	c, err := deps.TodayContent(context.Background())
	if err != nil {
		fmt.Fprintf(rw, "\x1b[31m오류: %s\x1b[0m\r\n\r\n", err)
		return
	}
	if c == nil {
		fmt.Fprint(rw, "\x1b[2m아직 등록된 질문이 없습니다.\x1b[0m\r\n\r\n")
		return
	}
	renderContent(rw, c)
}

func cmdList(rw io.ReadWriter, deps Deps) {
	if deps.ListContents == nil {
		fmt.Fprint(rw, "\x1b[31m콘텐츠 조회 기능이 설정되지 않았습니다.\x1b[0m\r\n\r\n")
		return
	}
	items, err := deps.ListContents(context.Background(), 10)
	if err != nil {
		fmt.Fprintf(rw, "\x1b[31m오류: %s\x1b[0m\r\n\r\n", err)
		return
	}
	if len(items) == 0 {
		fmt.Fprint(rw, "\x1b[2m아직 등록된 질문이 없습니다.\x1b[0m\r\n\r\n")
		return
	}
	fmt.Fprint(rw, "\r\n")
	for _, c := range items {
		tags := ""
		if len(c.Tags) > 0 {
			tags = "  \x1b[2m[" + strings.Join(c.Tags, ", ") + "]\x1b[0m"
		}
		fmt.Fprintf(rw, "  \x1b[1m%s\x1b[0m  %s%s\r\n", c.ContentID, c.Title, tags)
	}
	fmt.Fprint(rw, "\r\n")
}

func cmdSubscribe(rw io.ReadWriter, deps Deps, parts []string) {
	var email string
	if len(parts) >= 2 {
		email = strings.TrimSpace(strings.ToLower(parts[1]))
	} else {
		fmt.Fprint(rw, "이메일을 입력하세요: ")
		email = strings.TrimSpace(strings.ToLower(readLine(rw)))
		fmt.Fprint(rw, "\r\n")
	}
	if email == "" {
		return
	}
	if err := deps.Subscribe(context.Background(), email); err != nil {
		fmt.Fprintf(rw, "\x1b[31m오류: %s\x1b[0m\r\n\r\n", err)
		return
	}
	fmt.Fprint(rw, "\x1b[32m✓\x1b[0m 확인 메일을 보냈습니다. 메일함을 확인해주세요.\r\n\r\n")
}

func cmdShow(rw io.ReadWriter, deps Deps, parts []string) {
	if len(parts) < 2 {
		fmt.Fprint(rw, "사용법: show <id>  (예: show 0001)\r\n\r\n")
		return
	}
	if deps.GetContent == nil {
		fmt.Fprint(rw, "\x1b[31m콘텐츠 조회 기능이 설정되지 않았습니다.\x1b[0m\r\n\r\n")
		return
	}
	c, err := deps.GetContent(context.Background(), parts[1])
	if err != nil {
		fmt.Fprintf(rw, "\x1b[31m오류: %s\x1b[0m\r\n\r\n", err)
		return
	}
	if c == nil {
		fmt.Fprintf(rw, "\x1b[31m%s 를 찾을 수 없습니다.\x1b[0m\r\n\r\n", parts[1])
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

	rendered, err := glamour.Render(raw.String(), "dark")
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
		fmt.Fprintf(rw, "  \x1b[2m[%s]\x1b[0m\r\n", strings.Join(c.Tags, ", "))
	}
	fmt.Fprintf(rw, "  \x1b[1m%s\x1b[0m\r\n\r\n", c.Title)
	for _, line := range strings.Split(c.Preview, "\n") {
		fmt.Fprintf(rw, "  %s\r\n", line)
	}
	fmt.Fprint(rw, "\r\n")
}

func handleConfirmed(rw io.ReadWriter) {
	fmt.Fprint(rw,
		"\r\n"+
			"\x1b[32m✓ 구독이 완료됐습니다!\x1b[0m\r\n\r\n"+
			"\x1b[2m  내일부터 매일 아침 질문이 도착합니다.\x1b[0m\r\n\r\n",
	)
}

func handleUnsubscribe(rw io.ReadWriter, deps Deps, token string) {
	fmt.Fprint(rw, "\r\n구독을 취소하시겠어요? (y/n): ")
	line := readLine(rw)
	fmt.Fprintf(rw, "\r\n")
	if strings.TrimSpace(strings.ToLower(line)) != "y" {
		fmt.Fprint(rw, "취소하지 않았습니다.\r\n\r\n")
		return
	}
	if err := deps.Unsubscribe(context.Background(), token); err != nil {
		fmt.Fprintf(rw, "\x1b[31m오류: %s\x1b[0m\r\n", err)
		return
	}
	fmt.Fprint(rw, "\x1b[32m✓\x1b[0m 구독이 취소됐습니다.\r\n\r\n")
}

func readLine(rw io.ReadWriter) string {
	var buf []byte
	b := make([]byte, 1)
	for {
		n, err := rw.Read(b)
		if err != nil || n == 0 {
			return string(buf)
		}
		ch := b[0]
		if ch == '\r' || ch == '\n' {
			return string(buf)
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
