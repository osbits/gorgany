package graecoFramework

import (
	"context"
	"github.com/go-chi/chi"
	"net/http"
	"time"
)

var ServerTimezone *time.Location

type App struct {
	httpServer *http.Server
}

func (s *App) Run(router *chi.Mux, port string) error {
	s.httpServer = &http.Server{
		Addr:           ":" + port,
		Handler:        router,
		MaxHeaderBytes: 1 << 20, // 1 MB
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
	}

	return s.httpServer.ListenAndServe()
}

func (s *App) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
