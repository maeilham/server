package http

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/maeilham/server/internal/mail"
	"github.com/maeilham/server/internal/subscriber"
)

type Deps struct {
	Logger  *slog.Logger
	Store   *subscriber.Store
	Mailer  mail.Mailer
	BaseURL string
	APIURL  string
	Secret  string
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
		store:   deps.Store,
		mailer:  deps.Mailer,
		baseURL: deps.BaseURL,
		apiURL:  deps.APIURL,
		secret:  deps.Secret,
	}
	r.Post("/api/subscribe", sub.handleSubscribe)
	r.Get("/api/confirm", sub.handleConfirm)
	r.Post("/api/unsubscribe", sub.handleUnsubscribe)

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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
