package terminal

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"log/slog"
	"net"

	"golang.org/x/crypto/ssh"
)

// Server is a minimal SSH server that runs a TUI handler for each session.
type Server struct {
	config  *ssh.ServerConfig
	handler SessionHandler
	logger  *slog.Logger
}

// SessionHandler is called for each new SSH session with an io.ReadWriter and env vars.
type SessionHandler func(rw io.ReadWriter, env map[string]string)

func NewServer(logger *slog.Logger, handler SessionHandler) (*Server, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, fmt.Errorf("signer: %w", err)
	}

	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

	return &Server{config: cfg, handler: handler, logger: logger}, nil
}

func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	s.logger.Info("ssh server listening", "addr", addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(c net.Conn) {
	defer c.Close()

	sshConn, chans, reqs, err := ssh.NewServerConn(c, s.config)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "only session channels supported")
			continue
		}
		ch, requests, err := newChan.Accept()
		if err != nil {
			return
		}
		go func() {
			env := map[string]string{}
			for req := range requests {
				switch req.Type {
				case "env":
					var kv struct{ Name, Value string }
					if err := ssh.Unmarshal(req.Payload, &kv); err == nil {
						env[kv.Name] = kv.Value
					}
					if req.WantReply {
						req.Reply(true, nil)
					}
				case "pty-req":
					if req.WantReply {
						req.Reply(true, nil)
					}
				case "shell":
					if req.WantReply {
						req.Reply(true, nil)
					}
					go func() {
						s.handler(ch, env)
						ch.Close()
					}()
				default:
					if req.WantReply {
						req.Reply(false, nil)
					}
				}
			}
		}()
	}
}
