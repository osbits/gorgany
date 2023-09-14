package log

import (
	"log"
	"os"
)

type DefaultLogger struct {
}

func (thiz DefaultLogger) Info(v ...any) {
	log.SetPrefix("INFO ")
	log.Print(v...)
}

func (thiz DefaultLogger) Infof(format string, v ...any) {
	log.SetPrefix("INFO ")
	log.Printf(format, v...)
}

func (thiz DefaultLogger) Warn(v ...any) {
	log.SetPrefix("WARN ")
	log.Print(v...)
}

func (thiz DefaultLogger) Warnf(format string, v ...any) {
	log.SetPrefix("WARN ")
	log.Printf(format, v...)
}

func (thiz DefaultLogger) Error(v ...any) {
	log.SetPrefix("ERROR ")
	log.Print(v...)
	os.Exit(1)
}

func (thiz DefaultLogger) Errorf(format string, v ...any) {
	log.SetPrefix("ERROR ")
	log.Printf(format, v...)
	os.Exit(1)
}

func (thiz DefaultLogger) Panic(v ...any) {
	log.Panic(v...)
}

func (thiz DefaultLogger) Panicf(format string, v ...any) {
	log.Panicf(format, v...)
}

func (thiz DefaultLogger) Engine() any {
	return log.Default()
}
