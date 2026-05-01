package provider

import (
	"github.com/osbits/gorgany/app/core"
	logpkg "github.com/osbits/gorgany/log"
)

type LoggerProvider struct{}

func NewLoggerProvider() *LoggerProvider {
	return &LoggerProvider{}
}

func (p *LoggerProvider) Register(c core.IContainer) {
	c.SingletonLazy(func() core.Logger {
		return &logpkg.DefaultLogger{}
	})

	logpkg.SetLoggerFactory(func(key string) core.Logger {
		var logger core.Logger
		var err error

		if key == "" {
			// дефолтный
			err = c.Resolve(&logger)
		} else {
			// named
			err = c.NamedResolve(&logger, key)
		}
		if err == nil && logger != nil {
			return logger
		}
		// fallback: создаём и регистрируем сразу default
		defaultLogger := &logpkg.DefaultLogger{}
		_ = c.Singleton(func() core.Logger { return defaultLogger })
		return defaultLogger
	})

	// it's better to use NamedSingletonLazy for named loggers
	// c.NamedSingletonLazy("file", func() core.Logger { return NewFileLogger("app.log") })
}

func (p *LoggerProvider) Boot(c core.IContainer) {
}
