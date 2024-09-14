package log

import (
	"log"
)

type DefaultLogger struct {
	prefix string
}

func (thiz *DefaultLogger) SetPrefix(prefix string) {
	thiz.prefix = prefix
}

func (thiz *DefaultLogger) Info(v ...any) {
	log.SetPrefix(thiz.buildPrefix("INFO"))
	log.Print(v...)
}

func (thiz *DefaultLogger) Infof(format string, v ...any) {
	log.SetPrefix(thiz.buildPrefix("INFO"))
	log.Printf(format, v...)
}

func (thiz *DefaultLogger) Warn(v ...any) {
	log.SetPrefix(thiz.buildPrefix("WARN"))
	log.Print(v...)
}

func (thiz *DefaultLogger) Warnf(format string, v ...any) {
	log.SetPrefix(thiz.buildPrefix("WARN"))
	log.Printf(format, v...)
}

func (thiz *DefaultLogger) Error(v ...any) {
	log.SetPrefix(thiz.buildPrefix("ERROR"))
	log.Print(v...)
}

func (thiz *DefaultLogger) Errorf(format string, v ...any) {
	log.SetPrefix(thiz.buildPrefix("ERROR"))
	log.Printf(format, v...)
}

func (thiz *DefaultLogger) Panic(v ...any) {
	log.Panic(v...)
}

func (thiz *DefaultLogger) Panicf(format string, v ...any) {
	log.Panicf(format, v...)
}

func (thiz *DefaultLogger) Engine() any {
	return log.Default()
}

func (thiz *DefaultLogger) buildPrefix(level string) string {
	prefix := level + " "
	if thiz.prefix != "" {
		prefix = prefix + thiz.prefix + " "
	}
	return prefix
}
