package terminal

import (
	"context"
	"fmt"
	"io"
	"strings"
)

type Deps struct {
	Subscribe   func(ctx context.Context, email string) error
	Unsubscribe func(ctx context.Context, token string) error
}

func NewHandler(deps Deps) SessionHandler {
	return func(rw io.ReadWriter, env map[string]string) {
		action := env["MAEILHAM_ACTION"]
		token := env["MAEILHAM_TOKEN"]
		status := env["MAEILHAM_STATUS"]

		switch {
		case action == "unsubscribe" && token != "":
			handleUnsubscribe(rw, deps, token)
		case status == "confirmed":
			handleConfirmed(rw)
		default:
			handleSubscribe(rw, deps)
		}
	}
}

func handleSubscribe(rw io.ReadWriter, deps Deps) {
	fmt.Fprint(rw,
		"\r\n"+
			"\x1b[32m  _ __ ___   __ _  ___(_) | | |__   __ _ _ __ ___\x1b[0m\r\n"+
			"\x1b[32m | '_ ` _ \\ / _` |/ _ \\ | | | '_ \\ / _` | '_ ` _ \\\x1b[0m\r\n"+
			"\x1b[32m | | | | | | (_| |  __/ | | | | | | (_| | | | | | |\x1b[0m\r\n"+
			"\x1b[32m |_| |_| |_|\\__,_|\\___|_|_|_|_| |_|\\__,_|_| |_| |_|\x1b[0m\r\n"+
			"\r\n"+
			"\x1b[2m  매일 아침, 질문 하나가 도착합니다.\x1b[0m\r\n\r\n"+
			"이메일을 입력하세요: ",
	)

	email := readLine(rw)
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return
	}

	fmt.Fprintf(rw, "\r\n")
	if err := deps.Subscribe(context.Background(), email); err != nil {
		fmt.Fprintf(rw, "\x1b[31m오류: %s\x1b[0m\r\n", err.Error())
		return
	}
	fmt.Fprint(rw, "\x1b[32m✓\x1b[0m 확인 메일을 보냈습니다. 메일함을 확인해주세요.\r\n\r\n")
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
		fmt.Fprintf(rw, "\x1b[31m오류: %s\x1b[0m\r\n", err.Error())
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
		if ch == 127 || ch == 8 { // backspace
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
