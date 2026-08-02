package app

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/osbits/gorgany/v2"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/command"
	"github.com/osbits/gorgany/v2/config"
	"github.com/osbits/gorgany/v2/log"
	"github.com/osbits/gorgany/v2/service"
	"github.com/spf13/viper"
)

var Application core.IApplication

const (
	// ConfigMaxRequestBytes caps the raw request body, in bytes, before anything reads it.
	// Set it to lift or tighten the ceiling MaxRequestBodyBytes derives.
	ConfigMaxRequestBytes = "http.security.body.maxRequestBytes"

	// configMaxMultipartSize must stay spelled the same as http.ConfigMaxMultipartSize.
	// It cannot be referenced: the http package imports this one (http/error.go needs
	// GetRunMode), so the dependency only runs the other way. TestTheRequestCeilingTracks
	// TheMultipartBudget in http/ pins the two together.
	configMaxMultipartSize = "http.upload.maxMultipartSize"

	// DefaultMaxRequestBytes is the request-body ceiling when nothing is configured. It
	// matches http.DefaultMaxMultipartSize, so an app that configures no limits at all
	// gets one number rather than two that disagree.
	DefaultMaxRequestBytes int64 = 32 * 1024 * 1024

	// requestFramingAllowance is the slack the raw-body ceiling gets over the upload
	// budget it is derived from. A multipart body is bigger than the sum of the files in
	// it — part boundaries, headers and the trailing delimiter all count against a reader
	// limit but not against the upload budget — so a ceiling set to exactly the budget
	// would reject an upload that the upload limits allow.
	requestFramingAllowance int64 = 1 * 1024 * 1024
)

// MaxRequestBodyBytes is the ceiling every request body is held to.
//
// Nothing capped a request body before: the server was built with ReadTimeout,
// WriteTimeout and MaxHeaderBytes but no body limit, http.MaxBytesReader appeared nowhere
// in the framework, and the multipart parser read the whole form and consulted its size
// limits afterwards. The argument to ParseMultipartForm is only the in-memory budget —
// everything above it spills to temp files — so a single unauthenticated POST could write
// as many bytes to the OS temp directory as it cared to send, and the 10 MB per-file check
// ran once they were already on disk.
//
// The ceiling therefore tracks the upload budget rather than being an independent number:
// an app that raises http.upload.maxMultipartSize to accept larger uploads must not have
// them refused here instead, and an app that configures nothing gets a ceiling it will not
// notice. Configure ConfigMaxRequestBytes to override the derivation outright.
func MaxRequestBodyBytes() int64 {
	if configured := viper.GetInt64(ConfigMaxRequestBytes); configured > 0 {
		return configured
	}

	ceiling := DefaultMaxRequestBytes
	if multipartBudget := viper.GetInt64(configMaxMultipartSize); multipartBudget > ceiling {
		ceiling = multipartBudget
	}

	return ceiling + requestFramingAllowance
}

// limitRequestBodies caps every body at the server boundary, before routing and therefore
// before any handler, middleware or parser can read one. The framework applies the same
// cap per request when it builds the message, which is what protects an app embedding the
// router in a server of its own; this is the outer belt, and it also covers the paths that
// never reach a message at all.
func limitRequestBodies(next http.Handler, ceiling int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, ceiling)
		}
		next.ServeHTTP(w, r)
	})
}

func GetRunMode() gorgany.RunMode {
	mode := os.Getenv("MODE")
	if mode == "" {
		return gorgany.Dev
	}
	return gorgany.RunMode(mode)
}

type app struct {
	timezone     *time.Location
	bootstrapper core.Bootstrapper
	container    core.IContainer
	execType     gorgany.ExecType
}

func (s *app) Run() {
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

	s.container = service.NewContainer()
	s.bootstrapper.Bootstrap(s.container)

	s.log("").Infof("Gorgany framework is starting...\n\n")
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

func NewServerApp(bootstrapper core.Bootstrapper) *ServerApp {
	app := &ServerApp{}
	app.bootstrapper = bootstrapper
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

	port := viper.GetInt("app.server.port")
	if port == 0 {
		log.Log("").Panicf("Please specify SERVER_PORT in .env")
	}

	var router core.Router
	err := s.container.Resolve(&router)
	if err != nil {
		log.Log().Panicf("Failed to make router on start: %s", err.Error())
	}

	readTimeout := viper.GetDuration("app.server.timeout.read")
	if readTimeout == 0 {
		readTimeout = 60 * time.Second
	}
	writeTimeout := viper.GetDuration("app.server.timeout.write")
	if writeTimeout == 0 {
		writeTimeout = 60 * time.Second
	}

	maxRequestBytes := MaxRequestBodyBytes()

	go func() {
		s.httpServer = &http.Server{
			Addr:           fmt.Sprintf(":%d", port),
			Handler:        limitRequestBodies(router, maxRequestBytes),
			MaxHeaderBytes: 1 << 20,
			ReadTimeout:    readTimeout,
			WriteTimeout:   writeTimeout,
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

func NewConsoleApp(bootstrapper core.Bootstrapper) *ConsoleApp {
	app := &ConsoleApp{}
	app.bootstrapper = bootstrapper
	app.execType = gorgany.Cli
	Application = app
	return app
}

type ConsoleApp struct {
	app
}

func (s *ConsoleApp) Run() {
	s.app.Run()

	err := s.container.Invoke(func(resolver *command.Resolver) {
		if len(os.Args) < 2 {
			fmt.Println("Command name must be presented")
			return
		}
		cmd := resolver.ResolveCommand(os.Args[1])

		ctx := context.Background()

		cmd.Execute(ctx)
	})

	if err != nil {
		log.Log().Panicf("Failed to make resolver: %s", err.Error())
		return
	}
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
