package terminal

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"
	gossh "golang.org/x/crypto/ssh"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// WSBridge handles WebSocket connections and bridges them to the SSH server.
func WSBridge(logger *slog.Logger, sshAddr string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Warn("ws upgrade failed", "err", err)
			return
		}
		defer wsConn.Close()

		// SSH 서버에 연결
		sshConn, err := gossh.Dial("tcp", sshAddr, &gossh.ClientConfig{
			User:            "guest",
			Auth:            []gossh.AuthMethod{gossh.Password("")},
			HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		})
		if err != nil {
			logger.Warn("ssh dial failed", "err", err)
			wsConn.WriteMessage(websocket.TextMessage, []byte(
				fmt.Sprintf("\r\n\x1b[31m서버 연결 실패: %v\x1b[0m\r\n", err),
			))
			return
		}
		defer sshConn.Close()

		session, err := sshConn.NewSession()
		if err != nil {
			return
		}
		defer session.Close()

		// 쿼리파라미터를 SSH env로 전달
		if action := r.URL.Query().Get("action"); action != "" {
			session.Setenv("MAEILHAM_ACTION", action)
		}
		if token := r.URL.Query().Get("token"); token != "" {
			session.Setenv("MAEILHAM_TOKEN", token)
		}
		if status := r.URL.Query().Get("status"); status != "" {
			session.Setenv("MAEILHAM_STATUS", status)
		}

		// PTY 요청
		session.RequestPty("xterm-256color", 40, 120, gossh.TerminalModes{})

		stdin, _ := session.StdinPipe()
		stdout, _ := session.StdoutPipe()
		session.Shell()

		done := make(chan struct{}, 2)

		// SSH stdout → WebSocket
		go func() {
			defer func() { done <- struct{}{} }()
			buf := make([]byte, 4096)
			for {
				n, err := stdout.Read(buf)
				if n > 0 {
					wsConn.WriteMessage(websocket.BinaryMessage, buf[:n])
				}
				if err != nil {
					return
				}
			}
		}()

		// WebSocket → SSH stdin
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				_, msg, err := wsConn.ReadMessage()
				if err != nil {
					return
				}
				if _, err := io.WriteString(stdin, string(msg)); err != nil {
					return
				}
			}
		}()

		<-done
		stdin.Close() // SSH_MSG_CHANNEL_EOF → 서버 ch.Read() 언블록
	}
}
