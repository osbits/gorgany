package proxy

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
