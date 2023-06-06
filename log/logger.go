package log

type Logger interface {
	Info(v ...any)
	Infof(format string, v ...any)

	Warn(v ...any)
	Warnf(format string, v ...any)

	Error(v ...any)
	Errorf(format string, v ...any)

	Panic(v ...any)
	Panicf(format string, v ...any)

	Engine() any
}

var loggers = map[string]Logger{}

func SetLogger(loggerKey string, logger Logger) {
	loggers[loggerKey] = logger
}

// Log returns the Logger instance that was registered with the specified key, you need to check if the Logger is not null
func Log(loggerKey string) Logger {
	return loggers[loggerKey]
}
