package gorgany

import (
	"context"
	"fmt"
	"github.com/go-chi/chi"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

var ServerTimezone *time.Location

const ValidationSuccessMsg = "Successfully validated!"

type App struct {
	httpServer *http.Server
}

func (s *App) Run(router *chi.Mux, port string) error {
	err := s.validate()
	if err != nil {
		return err
	}

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

func (s *App) validate() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	fmtCmd := exec.Command("./grg", "validate-project", "-path", dir)
	res, err := fmtCmd.Output()
	resStr := string(res)
	if !strings.Contains(resStr, ValidationSuccessMsg) {
		return fmt.Errorf(resStr)
	}
	return nil
}
