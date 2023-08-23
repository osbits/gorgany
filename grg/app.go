package grg

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
	"gorgany"
	"gorgany/command"
	"gorgany/config"
	"gorgany/http/router"
	"gorgany/log"
	"gorgany/proxy"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"
)

var Application proxy.IApplication

func GetRunMode() gorgany.RunMode {
	mode := os.Getenv("MODE")
	if mode == "" {
		return gorgany.Dev
	}
	return gorgany.RunMode(mode)
}

type app struct {
	timezone    *time.Location
	appProvider proxy.IProvider
	execType    gorgany.ExecType
}

func (s *app) Run() {
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
		}
	}

	timezone, _ := time.LoadLocation(viper.GetString("app.server.timezone"))
	s.timezone = timezone

	s.appProvider.InitProvider()
}

func (s *app) ServerTimezone() *time.Location {
	return s.timezone
}

func (s *app) validate() error {
	s.log("").Infof("Validation of the generated files is performed")
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

func (s *app) log(key string) proxy.Logger {
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

func NewServerApp(appProvider proxy.IAppProvider) *ServerApp {
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
	s.app.Run()

	port := os.Getenv("SERVER_PORT")
	if port == "" {
		log.Log("").Panicf("Please specify SERVER_PORT in .env")
	}

	go func() {
		s.httpServer = &http.Server{
			Addr:           ":" + port,
			Handler:        router.GetRouter().Engine(),
			MaxHeaderBytes: 1 << 20,
			ReadTimeout:    10 * time.Second,
			WriteTimeout:   10 * time.Second,
		}

		log.Log("").Info("Server is doing to be up right now")
		if err := s.httpServer.ListenAndServe(); err != nil {
			log.Log("").Panicf("Error when starting the http server: %s", err.Error())
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

func NewConsoleApp(appProvider proxy.IAppProvider) *ConsoleApp {
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
	s.app.Run()

	resolver := command.NewCommandResolver()

	if len(os.Args) < 2 {
		fmt.Println("Command name must be presented")
		return
	}
	cmd := resolver.ResolveCommand(os.Args[1])
	cmd.Execute()
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
