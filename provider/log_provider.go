package provider

import "gorgany/log"

type LogProvider struct{}

func NewLogProvider() *LogProvider {
	return &LogProvider{}
}

func (thiz *LogProvider) InitProvider() {
	log.SetLogger("", log.DefaultLogger{})
	for key, logger := range FrameworkRegistrar.loggers {
		log.SetLogger(key, logger)
	}
}
