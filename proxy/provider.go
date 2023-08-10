package proxy

import (
	"gorgany/db"
)

type IProviders []IProvider

func (thiz IProviders) AddProvider(provider IProvider) {
	thiz = append(thiz, provider)
}

type IProvider interface {
	InitProvider()
}

type IRegistrar interface {
	SetHomeUrl(url string)
	GetHomeUrl() string
	RegisterController(controller IController)
	GetControllers() Controllers
	RegisterProvider(provider IProvider)
	GetProviders() IProviders
	RegisterDbConfig(dbType db.Type, config map[string]any)
	GetDbConfig(dbType db.Type) map[string]any
	RegisterCommand(command ICommand)
	GetCommands() ICommands
	SetSessionLifetime(lifetime int)
	GetSessionLifetime() int
	SetUserService(service IUserService)
	GetUserService() IUserService
	RegisterMiddleware(middleware IMiddleware)
	GetMiddlewares() []IMiddleware
	RegisterErrorHandler(errorType string, handler ErrorHandler)
	GetErrorHandlers() map[string]ErrorHandler
	RegisterLogger(key string, logger Logger)
	GetLoggers() map[string]Logger
	RegisterDomain(key string, domain interface{})
	GetDomains() map[string]interface{}
}
