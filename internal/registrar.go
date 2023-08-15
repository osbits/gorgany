package internal

import (
	"gorgany/proxy"
)

var frameworkRegistrar proxy.IRegistrar

func GetFrameworkRegistrar() proxy.IRegistrar {
	return frameworkRegistrar
}

func init() {
	frameworkRegistrar = &Registrar{
		controllers:         make(proxy.Controllers, 0),
		providers:           make(proxy.IProviders, 0),
		dbConnections:       make(map[proxy.DbType]proxy.IConnection),
		commands:            make(map[string]proxy.ICommand),
		middlewares:         make([]proxy.IMiddleware, 0),
		customErrorHandlers: make(map[string]proxy.ErrorHandler),
		loggers:             make(map[string]proxy.Logger),
		domains:             make(map[string]interface{}),
		migrations:          make([]proxy.IMigration, 0),
		seeders:             make([]proxy.ISeeder, 0),
	}
}

func SetRegistrar(registrar proxy.IRegistrar) {
	frameworkRegistrar = registrar
}

type Registrar struct {
	homeUrl             string
	controllers         proxy.Controllers
	providers           proxy.IProviders
	dbConnections       map[proxy.DbType]proxy.IConnection
	commands            map[string]proxy.ICommand
	sessionLifetime     int //in seconds
	userService         proxy.IUserService
	middlewares         []proxy.IMiddleware
	customErrorHandlers map[string]proxy.ErrorHandler
	loggers             map[string]proxy.Logger
	domains             map[string]interface{}
	migrations          []proxy.IMigration
	seeders             []proxy.ISeeder
	sessionStorage      proxy.ISessionStorage
	i18nManager         proxy.Ii18nManager
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

func (thiz *Registrar) RegisterCommand(command proxy.ICommand) {
	if thiz.commands == nil {
		thiz.commands = make(map[string]proxy.ICommand)
	}

	thiz.commands[command.GetName()] = command
}

func (thiz *Registrar) GetCommands() map[string]proxy.ICommand {
	return thiz.commands
}

func (thiz *Registrar) GetCommand(name string) proxy.ICommand {
	return thiz.commands[name]
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

func (thiz *Registrar) RegisterMigration(migration proxy.IMigration) {
	thiz.migrations = append(thiz.migrations, migration)
}

func (thiz *Registrar) GetMigrations() []proxy.IMigration {
	return thiz.migrations
}

func (thiz *Registrar) RegisterSeeder(seeder proxy.ISeeder) {
	thiz.seeders = append(thiz.seeders, seeder)
}

func (thiz *Registrar) GetSeeders() []proxy.ISeeder {
	return thiz.seeders
}

func (thiz *Registrar) SetSessionStorage(sessionStorage proxy.ISessionStorage) {
	thiz.sessionStorage = sessionStorage
}

func (thiz *Registrar) GetSessionStorage() proxy.ISessionStorage {
	return thiz.sessionStorage
}

func (thiz *Registrar) SetI18nManager(manager proxy.Ii18nManager) {
	thiz.i18nManager = manager
}

func (thiz *Registrar) GetI18nManager() proxy.Ii18nManager {
	return thiz.i18nManager
}

func (thiz *Registrar) RegisterDbConnection(kind proxy.DbType, connection proxy.IConnection) {
	thiz.dbConnections[kind] = connection
}

func (thiz *Registrar) GetDbConnections() map[proxy.DbType]proxy.IConnection {
	return thiz.dbConnections
}

func (thiz *Registrar) GetDbConnection(kind proxy.DbType) proxy.IConnection {
	return thiz.dbConnections[kind]
}
