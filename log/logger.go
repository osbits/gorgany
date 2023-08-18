package log

import (
	"gorgany/internal"
	"gorgany/proxy"
)

// Log returns the Logger instance that was registered with the specified key, you need to check if the Logger is not null
func Log(loggerKey string) proxy.Logger {
	logger := internal.GetFrameworkRegistrar().GetLogger(loggerKey)
	if logger == nil && loggerKey == "" {
		defaultLogger := &DefaultLogger{}
		internal.GetFrameworkRegistrar().RegisterLogger("", defaultLogger)
		return defaultLogger
	}
	return logger
}
