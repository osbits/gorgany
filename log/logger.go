package log

import (
	"gorgany/app/core"
	"gorgany/internal"
)

// Log returns the Logger instance that was registered with the specified key, you need to check if the Logger is not null
func Log(loggerKey ...string) core.Logger {
	key := ""
	if len(loggerKey) > 0 {
		key = loggerKey[0]
	}

	logger := internal.GetFrameworkRegistrar().GetLogger(key)
	if logger == nil && key == "" {
		defaultLogger := &DefaultLogger{}
		internal.GetFrameworkRegistrar().RegisterLogger("", defaultLogger)
		return defaultLogger
	}
	return logger
}
