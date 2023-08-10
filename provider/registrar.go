package provider

import (
	"gorgany/db"
	"gorgany/proxy"
	"log"
)

var FrameworkRegistrar proxy.IRegistrar

type Registrar struct {
	homeUrl             string
	controllers         proxy.Controllers
	providers           proxy.IProviders
	dbConfigs           map[db.Type]map[string]any // dbConfigs = { "postgres": {"host": "localhost", "port": "5432"...}, "mongo": {"host": "localhost"}
	commands            proxy.ICommands
	sessionLifetime     int //in seconds
	userService         proxy.IUserService
	middlewares         []proxy.IMiddleware
	customErrorHandlers map[string]proxy.ErrorHandler
	loggers             map[string]proxy.Logger
	domains             map[string]interface{}
}

func InitRegistrar() {
	FrameworkRegistrar = &Registrar{
		controllers: make(proxy.Controllers, 0),
		providers:   make(proxy.IProviders, 0),
		dbConfigs:   make(map[db.Type]map[string]any, 0),
	}
}

func (thiz *Registrar) SetHomeUrl(url string) {
	thiz.homeUrl = url
}

func (thiz *Registrar) GetHomeUrl() string {
	return thiz.homeUrl
}

type T struct {
	Registrar
}

func (thiz *Registrar) RegisterController(controller proxy.IController) {
	thiz.controllers = append(thiz.controllers, controller)
}

func (thiz *Registrar) GetControllers() proxy.Controllers {
	return thiz.controllers
}

func (thiz *Registrar) RegisterProvider(provider proxy.IProvider) {
	thiz.providers = append(thiz.providers, provider)
}

func (thiz *Registrar) GetProviders() proxy.IProviders {
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

func (thiz *Registrar) RegisterCommand(command proxy.ICommand) {
	thiz.commands = append(thiz.commands, command)
}

func (thiz *Registrar) GetCommands() proxy.ICommands {
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

func (thiz *Registrar) SetUserService(service proxy.IUserService) {
	thiz.userService = service
}

func (thiz *Registrar) GetUserService() proxy.IUserService {
	return thiz.userService
}

func (thiz *Registrar) RegisterMiddleware(middleware proxy.IMiddleware) {
	if thiz.middlewares == nil {
		thiz.middlewares = make([]proxy.IMiddleware, 0)
	}
	thiz.middlewares = append(thiz.middlewares, middleware)
}

func (thiz *Registrar) GetMiddlewares() []proxy.IMiddleware {
	return thiz.middlewares
}

func (thiz *Registrar) RegisterErrorHandler(errorType string, handler proxy.ErrorHandler) {
	if thiz.customErrorHandlers == nil {
		thiz.customErrorHandlers = make(map[string]proxy.ErrorHandler)
	}
	thiz.customErrorHandlers[errorType] = handler
}

func (thiz *Registrar) GetErrorHandlers() map[string]proxy.ErrorHandler {
	return thiz.customErrorHandlers
}

func (thiz *Registrar) RegisterLogger(key string, logger proxy.Logger) {
	if thiz.loggers == nil {
		thiz.loggers = make(map[string]proxy.Logger)
	}
	thiz.loggers[key] = logger
}

func (thiz *Registrar) GetLoggers() map[string]proxy.Logger {
	return thiz.loggers
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
