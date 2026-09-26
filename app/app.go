package app

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/osbits/gorgany/v2"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/command"
	"github.com/osbits/gorgany/v2/config"
	"github.com/osbits/gorgany/v2/job"
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

	// ConfigShutdownTimeout bounds a graceful shutdown: how long ServerApp waits, after
	// SIGTERM or SIGINT, for in-flight requests, running jobs and async event subscribers
	// before it gives up on them and exits with status 1. Keep it below the grace period
	// of whatever sends the signal, or that sends SIGKILL first.
	ConfigShutdownTimeout = "app.server.timeout.shutdown"

	// DefaultShutdownTimeout is the shutdown deadline when nothing is configured. It
	// matches Kubernetes' default termination grace period.
	DefaultShutdownTimeout = 30 * time.Second
)

// ShutdownTimeout is the configured ConfigShutdownTimeout, or DefaultShutdownTimeout when
// that is unset or not positive.
func ShutdownTimeout() time.Duration {
	if configured := viper.GetDuration(ConfigShutdownTimeout); configured > 0 {
		return configured
	}
	return DefaultShutdownTimeout
}

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

	// What Shutdown drains after the HTTP server. Each is resolved once the providers have
	// booted, and is nil when the application binds none.
	scheduler *job.Scheduler
	eventBus  core.IEventBus
	dbContext core.IDBContext

	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
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

	// Built here rather than on the serving goroutine, where it used to be: Shutdown reads
	// the field from this goroutine, and a signal that arrived before the other one had
	// assigned it would have found nil.
	s.httpServer = &http.Server{
		Addr:           fmt.Sprintf(":%d", port),
		Handler:        limitRequestBodies(router, maxRequestBytes),
		MaxHeaderBytes: 1 << 20,
		ReadTimeout:    readTimeout,
		WriteTimeout:   writeTimeout,
	}
	s.resolveShutdownTargets()

	listener, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		log.Log().Panicf("Error while the http server is running: %s", err.Error())
	}

	signals, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stopSignals()
	// Once the first signal has started the shutdown, a second one gets its default
	// behaviour back and ends the process at once: the way out, from a terminal, of a drain
	// that is taking too long.
	context.AfterFunc(signals, stopSignals)

	log.Log().Infof("Server is running on port\u001B[0;32m :%d \u001B[0m", port)
	if err := s.serve(signals, listener, ShutdownTimeout()); err != nil {
		// errors.Join puts one error per line; a log line is one line.
		log.Log("").Errorf("Error occurred on server shutting down: %s",
			strings.ReplaceAll(err.Error(), "\n", "; "))
		os.Exit(1)
	}
	log.Log().Infof("Server stopped")
}

// serve serves on listener until stop is done, then shuts down within timeout.
//
// Serve returns http.ErrServerClosed as soon as Shutdown closes the listener, which is
// before Shutdown has finished. Run used to hand that error to Panicf on the serving
// goroutine, so every SIGTERM logged it, and one that arrived mid-request crashed the
// process with exit status 2 and cut the request off. It is the normal end of serving;
// what matters is that Shutdown completes, and this returns only once it has.
func (s *ServerApp) serve(stop context.Context, listener net.Listener, timeout time.Duration) error {
	served := make(chan error, 1)
	go func() {
		served <- s.httpServer.Serve(listener)
	}()

	select {
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server stopped serving: %w", err)
		}
		// Application code called Shutdown. Returning now would let main exit in the
		// middle of that drain, so wait for it.
		return s.Shutdown(context.Background())
	case <-stop.Done():
	}

	log.Log().Infof("Shutting down: waiting up to %s for requests, jobs and async events", timeout)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	err := s.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("not finished within %s (%s): %w", timeout, ConfigShutdownTimeout, err)
	}
	return err
}

// Shutdown drains the server within ctx. Later and concurrent calls wait for the first
// one, until their own ctx is done, and return its result.
func (s *ServerApp) Shutdown(ctx context.Context) error {
	first := false
	s.shutdownOnce.Do(func() {
		first = true
		s.shutdownDone = make(chan struct{})
	})

	// The first caller runs the drain itself. Every step gives up when ctx is done, so
	// there is nothing to race ctx against, and doing so would return a bare deadline
	// error in place of the one that names what was still running.
	if first {
		s.shutdownErr = s.shutdown(ctx)
		close(s.shutdownDone)
		return s.shutdownErr
	}

	select {
	case <-s.shutdownDone:
		return s.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// shutdown stops the work the framework started, in this order, all within ctx:
//
//  1. The HTTP server stops accepting and waits for in-flight requests.
//  2. The job scheduler cancels the context each running job was given, and waits for
//     the jobs to return.
//  3. The event bus waits for async subscribers. Requests and jobs both start them, so it
//     goes after both.
//  4. The datasources close, once nothing is left to use them.
func (s *ServerApp) shutdown(ctx context.Context) error {
	var errs []error

	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("http server: %w", err))
			// Shutdown leaves open the connections it gave up waiting for. Closing them
			// fails those requests now rather than whenever the process exits.
			_ = s.httpServer.Close()
		}
	}

	var drains []drain
	if s.scheduler != nil {
		drains = append(drains, drain{"job scheduler", s.scheduler.Stop})
	}
	if s.eventBus != nil {
		drains = append(drains, drain{"event bus", s.eventBus.WaitAsync})
	}
	for _, d := range drains {
		// Past the deadline a step is still started, so the scheduler cancels its jobs, but
		// not waited for. Racing it against a context that is already done would report
		// an idle step as timed out half the time.
		if ctx.Err() != nil {
			go d.wait()
			errs = append(errs, fmt.Errorf("%s: not waited for: %w", d.name, ctx.Err()))
			continue
		}
		if err := waitUntil(ctx, d.wait); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", d.name, err))
		}
	}

	// After a timeout something may still be running a query, and the process is about to
	// exit anyway, so the datasources are closed only when everything above finished.
	if closer, ok := s.dbContext.(io.Closer); ok && len(errs) == 0 {
		if err := closer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("datasources: %w", err))
		}
	}

	return errors.Join(errs...)
}

// resolveShutdownTargets looks up what Shutdown drains besides the HTTP server. The
// providers have booted by now, so a missing binding is one the application does not have.
func (s *ServerApp) resolveShutdownTargets() {
	// The container constructs a pointer-to-struct type nobody bound, so without JobProvider
	// this is an empty scheduler that never started. One with no jobs has nothing to stop.
	if err := s.container.Resolve(&s.scheduler); err != nil || len(s.scheduler.Jobs()) == 0 {
		s.scheduler = nil
	}

	var bus core.IEventBus
	if err := s.container.Resolve(&bus); err == nil {
		s.eventBus = bus
	}

	var dbContext core.IDBContext
	if err := s.container.Resolve(&dbContext); err == nil {
		s.dbContext = dbContext
	}
}

// drain is a shutdown step that waits for work to finish.
type drain struct {
	name string
	wait func()
}

// waitUntil runs fn and waits until it returns or ctx is done. After a timeout fn is left
// running; the process exits shortly afterwards.
func waitUntil(ctx context.Context, fn func()) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
	// Checked before booting, which opens the database pools for nothing. The exit status is
	// the point: this used to print and exit 0, so a deploy step that lost its arguments
	// passed. 2 is what a bad flag exits with, through the resolver's flag.ExitOnError.
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Command name must be presented: %s <command> [--flag=value ...]\n",
			filepath.Base(os.Args[0]))
		os.Exit(2)
	}

	s.app.Run()

	err := s.container.Invoke(func(resolver *command.Resolver) {
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
