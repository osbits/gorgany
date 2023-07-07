package provider

import (
	"gorgany/auth/service"
	"gorgany/command"
	"gorgany/db"
	"gorgany/http"
	log2 "gorgany/log"
	"log"
)

var FrameworkRegistrar *Registrar

type Registrar struct {
	controllers         http.Controllers
	providers           IProviders
	dbConfigs           map[db.Type]map[string]any // dbConfigs = { "postgres": {"host": "localhost", "port": "5432"...}, "mongo": {"host": "localhost"}
	commands            command.ICommands
	sessionLifetime     int //in seconds
	userService         service.IUserService
	middlewares         []http.IMiddleware
	customErrorHandlers map[string]http.ErrorHandler
	loggers             map[string]log2.Logger
	domains             map[string]interface{}
}

func InitRegistrar() {
	FrameworkRegistrar = &Registrar{
		controllers: make(http.Controllers, 0),
		providers:   make(IProviders, 0),
		dbConfigs:   make(map[db.Type]map[string]any, 0),
	}
}

func (thiz *Registrar) RegisterController(controller http.IController) {
	thiz.controllers = append(thiz.controllers, controller)
}

func (thiz *Registrar) GetControllers() http.Controllers {
	return thiz.controllers
}

func (thiz *Registrar) RegisterProvider(provider IProvider) {
	thiz.providers = append(thiz.providers, provider)
}

func (thiz *Registrar) GetProviders() IProviders {
	return thiz.providers
}

func (thiz *Registrar) RegisterDbConfig(dbType db.Type, config map[string]any) {
	thiz.dbConfigs[dbType] = make(map[string]any, 0)
	thiz.dbConfigs[dbType] = config
}

func (thiz *Registrar) GetDbConfig(dbType db.Type) map[string]any {
	config, ok := thiz.dbConfigs[dbType]
	if !ok {
		log.Panicf("Config for %v does not exist", dbType)
	}
	return config
}

func (thiz *Registrar) RegisterCommand(command command.ICommand) {
	thiz.commands = append(thiz.commands, command)
}

func (thiz *Registrar) GetCommands() command.ICommands {
	return thiz.commands
}

func (thiz *Registrar) SetSessionLifetime(lifetime int) {
	thiz.sessionLifetime = lifetime
}

func (thiz *Registrar) GetSessionLifetime() int {
	if thiz.sessionLifetime == 0 {
		return 3600
	}
	return thiz.sessionLifetime
}

func (thiz *Registrar) SetUserService(service service.IUserService) {
	thiz.userService = service
}

func (thiz *Registrar) GetUserService() service.IUserService {
	return thiz.userService
}

func (thiz *Registrar) RegisterMiddleware(middleware http.IMiddleware) {
	if thiz.middlewares == nil {
		thiz.middlewares = make([]http.IMiddleware, 0)
	}
	thiz.middlewares = append(thiz.middlewares, middleware)
}

func (thiz *Registrar) GetMiddlewares() []http.IMiddleware {
	return thiz.middlewares
}

func (thiz *Registrar) RegisterErrorHandler(errorType string, handler http.ErrorHandler) {
	if thiz.customErrorHandlers == nil {
		thiz.customErrorHandlers = make(map[string]http.ErrorHandler)
	}
	thiz.customErrorHandlers[errorType] = handler
}

func (thiz *Registrar) RegisterLogger(key string, logger log2.Logger) {
	if thiz.loggers == nil {
		thiz.loggers = make(map[string]log2.Logger)
	}
	thiz.loggers[key] = logger
}

func (thiz *Registrar) RegisterDomain(key string, domain interface{}) {
	if thiz.domains == nil {
		thiz.domains = make(map[string]interface{})
	}
	thiz.domains[key] = domain
}

func (thiz *Registrar) GetDomains() map[string]interface{} {
	return thiz.domains
}
