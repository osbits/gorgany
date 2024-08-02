package app

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/command"
	"git.qix.sx/gorgany/gorgany.git/config"
	"git.qix.sx/gorgany/gorgany.git/http/router"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"git.qix.sx/gorgany/gorgany.git/log"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"
)

var Application core.IApplication

func GetRunMode() gorgany.RunMode {
	mode := os.Getenv("MODE")
	if mode == "" {
		return gorgany.Dev
	}
	return gorgany.RunMode(mode)
}

type app struct {
	timezone    *time.Location
	appProvider core.IAppProvider
	execType    gorgany.ExecType
}

func (s *app) Run(applicationContext core.IApplicationContext) {
	s.log("").Infof("Gorgany framework is starting...\n\n")
	if err := godotenv.Load(); err != nil {
		s.log("").Panicf("Failed load env file: %s", err.Error())
	}

	if err := config.Parse("config/config"); err != nil {
		s.log("").Panicf("Failed load config file: %s", err.Error())
	}
	if viper.GetBool("app.gorgany.validate") {
		err := s.validate()
		if err != nil {
			s.log("").Error(err)
			os.Exit(1)
		}
	}

	timezone, _ := time.LoadLocation(viper.GetString("app.server.timezone"))
	s.timezone = timezone

	s.appProvider.InitProvider(applicationContext)
}

func (s *app) ServerTimezone() *time.Location {
	return s.timezone
}

func (s *app) validate() error {
	s.log("").Infof("Validation of the generated files is performing")
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
			s.log("").Warnf("File %s(%s) has been modified and the checksum does not match the generated copy!\n", file.Name, file.Path)
		}
	}

	if !success {
		return fmt.Errorf("\u001B[0;31mValidation error\u001B[0m")
	}
	s.log("").Infof("\u001B[0;32mSuccessfully validated\u001B[0m\n\n")

	return nil
}

func (s *app) parseLock() (*Lock, error) {
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

func (s *app) log(key string) core.Logger {
	//if s.execType == gorgany.Cli {
	//	defaultLogger := &log.DefaultLogger{}
	//	castedLogger := defaultLogger.Engine().(*log2.Logger)
	//	castedLogger.SetOutput(&EmptyWriter{})
	//	return defaultLogger
	//}

	return log.Log(key)
}

// Lock file
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

// Server application

func NewServerApp(appProvider core.IAppProvider) *ServerApp {
	app := &ServerApp{}
	app.appProvider = appProvider
	app.execType = gorgany.Server
	Application = app
	return app
}

type ServerApp struct {
	app
	httpServer *http.Server
}

func (s *ServerApp) Run() {
	applicationContext := internal.InitApplicationContext()

	s.app.Run(applicationContext)

	port := viper.GetInt("app.server.port")
	if port == 0 {
		log.Log("").Panicf("Please specify SERVER_PORT in .env")
	}

	go func() {
		s.httpServer = &http.Server{
			Addr:           fmt.Sprintf(":%d", port),
			Handler:        router.GetRouter().Engine(),
			MaxHeaderBytes: 1 << 20,
			ReadTimeout:    10 * time.Second,
			WriteTimeout:   10 * time.Second,
		}

		log.Log().Infof("Server is running on port\u001B[0;32m :%d \u001B[0m", port)
		if err := s.httpServer.ListenAndServe(); err != nil {
			log.Log().Panicf("Error while the http server is running: %s", err.Error())
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit
	if err := s.Shutdown(context.Background()); err != nil {
		log.Log("").Errorf("Error occurred on server shutting down: %s", err.Error())
	}
}

func (s *ServerApp) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

//Console application

func NewConsoleApp(appProvider core.IAppProvider) *ConsoleApp {
	app := &ConsoleApp{}
	app.appProvider = appProvider
	app.execType = gorgany.Cli
	Application = app
	return app
}

type ConsoleApp struct {
	app
}

func (s *ConsoleApp) Run() {
	applicationContext := internal.InitApplicationContext()

	s.app.Run(applicationContext)

	resolver := command.NewCommandResolver(applicationContext)

	if len(os.Args) < 2 {
		fmt.Println("Command name must be presented")
		return
	}
	cmd := resolver.ResolveCommand(os.Args[1])
	cmd.Execute(context.WithValue(context.Background(), core.ApplicationContextKey, applicationContext))
}

func (s *ConsoleApp) Shutdown(ctx context.Context) error {
	os.Exit(1)
	return nil
}

// Empty writer

type EmptyWriter struct{}

func (c *EmptyWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}
