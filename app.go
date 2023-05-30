package gorgany

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

var ServerTimezone *time.Location

type App struct {
	httpServer *http.Server
}

func (s *App) Run(router *chi.Mux, port string) error {
	if viper.GetBool("app.gorgany.validate") {
		err := s.validate()
		if err != nil {
			return err
		}
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
	lock, err := s.parseLock()
	if err != nil {
		return err
	}

	success := true
	for _, file := range lock.Files {
		content, err := os.ReadFile(path.Join(file.Path, file.Name))
		if err != nil {
			if strings.Contains(err.Error(), "no such file or directory") {
				return err
			}
			return err
		}
		checksum := md5.Sum(content)
		checksumStr := hex.EncodeToString(checksum[:])
		if checksumStr != file.Checksum {
			success = false
			fmt.Printf("File %s(%s) has been modified and the checksum does not match the generated copy!\n", file.Name, file.Path)
		}
	}

	if !success {
		return fmt.Errorf("Validation error")
	}

	return nil
}

type Lock struct {
	Name    string
	Version int `json:"lockfileVersion"`
	Files   []LockFiles
}

type LockFiles struct {
	Name     string
	Path     string
	Checksum string
}

func (s *App) parseLock() (*Lock, error) {
	content, err := os.ReadFile("grg-lock.json")
	if err != nil {
		if strings.Contains(err.Error(), "no such file or directory") {
			return &Lock{}, nil
		}
		return nil, err
	}

	lock := new(Lock)
	err = json.Unmarshal(content, lock)
	if err != nil {
		return nil, err
	}
	return lock, nil
}
