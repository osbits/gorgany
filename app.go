package graecoFramework

import (
	"context"
	http2 "graecoFramework/http"
	"net/http"
	"time"
)

type App struct {
	httpServer *http.Server
}

func (s *App) Run(port string) error {
	dispatcher := http2.Dispatcher{}
	s.httpServer = &http.Server{
		Addr:           ":" + port,
		Handler:        dispatcher,
		MaxHeaderBytes: 1 << 20, // 1 MB
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
	}

	return s.httpServer.ListenAndServe()
}

func (s *App) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
