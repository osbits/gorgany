package core

// Logger defines the interface for logging operations
type Logger interface {
	// SetPrefix sets the prefix for log messages
	SetPrefix(prefix string)

	// Info logs an informational message
	Info(v ...any)
	// Infof logs a formatted informational message
	Infof(format string, v ...any)

	// Warn logs a warning message
	Warn(v ...any)
	// Warnf logs a formatted warning message
	Warnf(format string, v ...any)

	// Error logs an error message
	Error(v ...any)
	// Errorf logs a formatted error message
	Errorf(format string, v ...any)

	// Panic logs a panic message and then panics
	Panic(v ...any)
	// Panicf logs a formatted panic message and then panics
	Panicf(format string, v ...any)

	// Engine returns the underlying logging engine
	Engine() any
}
