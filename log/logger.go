package log

import "gorgany/proxy"

var loggers = map[string]proxy.Logger{}

func SetLogger(loggerKey string, logger proxy.Logger) {
	loggers[loggerKey] = logger
}

// Log returns the Logger instance that was registered with the specified key, you need to check if the Logger is not null
func Log(loggerKey string) proxy.Logger {
	return loggers[loggerKey]
}
