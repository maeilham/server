package terminal

import (
	"fmt"
	"io"
)

// WelcomeHandler is a temporary session handler that shows a greeting and echoes input.
// Replace with real bubbletea TUI later.
func WelcomeHandler(rw io.ReadWriter) {
	welcome := "\r\n" +
		"\x1b[32m  _ __ ___   __ _  ___(_) | | |__   __ _ _ __ ___\x1b[0m\r\n" +
		"\x1b[32m | '_ ` _ \\ / _` |/ _ \\ | | | '_ \\ / _` | '_ ` _ \\\x1b[0m\r\n" +
		"\x1b[32m | | | | | | (_| |  __/ | | | | | | (_| | | | | | |\x1b[0m\r\n" +
		"\x1b[32m |_| |_| |_|\\__,_|\\___|_|_|_|_| |_|\\__,_|_| |_| |_|\x1b[0m\r\n" +
		"\r\n" +
		"\x1b[2m  매일 아침, 질문 하나가 도착합니다.\x1b[0m\r\n\r\n" +
		"이메일을 입력하세요: "

	fmt.Fprint(rw, welcome)

	// 이메일 한 줄 읽기
	var email []byte
	buf := make([]byte, 1)
	for {
		n, err := rw.Read(buf)
		if err != nil || n == 0 {
			return
		}
		ch := buf[0]
		if ch == '\r' || ch == '\n' {
			break
		}
		if ch == 127 || ch == 8 { // backspace
			if len(email) > 0 {
				email = email[:len(email)-1]
				fmt.Fprint(rw, "\b \b")
			}
			continue
		}
		email = append(email, ch)
		fmt.Fprint(rw, string(ch)) // echo
	}

	fmt.Fprintf(rw, "\r\n\r\n\x1b[33m%s\x1b[0m 로 확인 메일을 발송합니다...\r\n", string(email))
	fmt.Fprint(rw, "\x1b[32m✓ 완료!\x1b[0m 메일함을 확인해주세요.\r\n\r\n")
}
