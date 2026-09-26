package http

import (
	"log/slog"
	"net/http"
	_ "net/http/pprof"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/maeilham/server/internal/content"
	"github.com/maeilham/server/internal/store"
	"github.com/maeilham/server/internal/subscriber"
	"github.com/maeilham/server/internal/terminal"
)

type Deps struct {
	Logger  *slog.Logger
	SubSvc  *subscriber.SubscriberService
	BaseURL string
	SSHAddr string // SSH 서버 주소 (WebSocket 브리지용)

	Contents store.ContentRepository // 콘텐츠 메타데이터 조회
	Bodies   content.BodySource      // 콘텐츠 본문(마크다운) 조회
	Today    TodayPicker             // 오늘의 질문 선정
}

func NewRouter(deps Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)
	r.Use(corsMiddleware)
	r.Use(slogMiddleware(deps.Logger))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	sub := &subscribeHandler{
		subSvc:  deps.SubSvc,
		baseURL: deps.BaseURL,
		logger:  deps.Logger,
	}
	r.Post("/api/subscribe", sub.handleSubscribe)
	r.Get("/api/confirm", sub.handleConfirm)
	r.Post("/api/unsubscribe", sub.handleUnsubscribe)

	sess := &sessionHandler{subSvc: deps.SubSvc, logger: deps.Logger}
	r.Post("/api/session", sess.handleSession)
	r.Get("/api/me", sess.handleMe)

	contents := &contentHandler{contents: deps.Contents, bodies: deps.Bodies, logger: deps.Logger}
	today := &todayHandler{picker: deps.Today, logger: deps.Logger}
	r.Get("/api/today", today.handleGet)

	r.Get("/api/contents", contents.handleList)
	r.Get("/api/contents/{repo}/{id}", contents.handleGet)

	sshAddr := deps.SSHAddr
	if sshAddr == "" {
		sshAddr = "localhost:2222"
	}
	r.Get("/ws/terminal", terminal.WSBridge(deps.Logger, sshAddr))

	r.Mount("/debug", chimw.Profiler())

	return r
}

func slogMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
			)
		})
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
