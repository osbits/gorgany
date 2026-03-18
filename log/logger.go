package log

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
)

var (
	loggerFactory func(key string) core.Logger
)

func SetLoggerFactory(f func(key string) core.Logger) {
	if loggerFactory != nil {
		panic("log: factory already set")
	}
	loggerFactory = f
}

func Log(key ...string) core.Logger {
	if loggerFactory == nil {
		return &DefaultLogger{}
	}

	k := core.DefaultKeyInRegistrar

	if len(key) > 0 {
		k = key[0]
	}

	return loggerFactory(k)
}
