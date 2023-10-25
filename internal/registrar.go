package internal

import (
	"gorgany/app/core"
)

var frameworkRegistrar core.IRegistrar

func GetFrameworkRegistrar() core.IRegistrar {
	return frameworkRegistrar
}

func init() {
	frameworkRegistrar = &Registrar{
		controllers:         make(core.Controllers, 0),
		providers:           make(core.IProviders, 0),
		dbConnections:       make(map[core.DbType]core.IConnection),
		commands:            make(map[string]core.ICommand),
		middlewares:         make([]core.IMiddleware, 0),
		customErrorHandlers: make(map[string]core.ErrorHandler),
		loggers:             make(map[string]core.Logger),
		domains:             make(map[string]interface{}),
		migrations:          make([]core.IMigration, 0),
		seeders:             make([]core.ISeeder, 0),
	}
}

func SetRegistrar(registrar core.IRegistrar) {
	frameworkRegistrar = registrar
}

type Registrar struct {
	homeUrl             string
	controllers         core.Controllers
	providers           core.IProviders
	dbConnections       map[core.DbType]core.IConnection
	commands            map[string]core.ICommand
	sessionLifetime     int //in seconds
	userService         core.IUserService
	middlewares         []core.IMiddleware
	customErrorHandlers map[string]core.ErrorHandler
	loggers             map[string]core.Logger
	domains             map[string]interface{}
	migrations          []core.IMigration
	seeders             []core.ISeeder
	sessionStorage      core.ISessionStorage
	i18nManager         core.Ii18nManager
	viewEngine          core.IViewEngine
	router              core.Router
	container           core.IContainer
	eventBus            core.IEventBus
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

func (thiz *Registrar) RegisterController(controller core.IController) {
	thiz.controllers = append(thiz.controllers, controller)
}

func (thiz *Registrar) GetControllers() core.Controllers {
	return thiz.controllers
}

func (thiz *Registrar) RegisterProvider(provider core.IProvider) {
	thiz.providers = append(thiz.providers, provider)
}

func (thiz *Registrar) GetProviders() core.IProviders {
	return thiz.providers
}

func (thiz *Registrar) RegisterCommand(command core.ICommand) {
	if thiz.commands == nil {
		thiz.commands = make(map[string]core.ICommand)
	}

	thiz.commands[command.GetName()] = command
}

func (thiz *Registrar) GetCommands() map[string]core.ICommand {
	return thiz.commands
}

func (thiz *Registrar) GetCommand(name string) core.ICommand {
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

func (thiz *Registrar) SetUserService(service core.IUserService) {
	thiz.userService = service
}

func (thiz *Registrar) GetUserService() core.IUserService {
	return thiz.userService
}

func (thiz *Registrar) RegisterMiddleware(middleware core.IMiddleware) {
	if thiz.middlewares == nil {
		thiz.middlewares = make([]core.IMiddleware, 0)
	}
	thiz.middlewares = append(thiz.middlewares, middleware)
}

func (thiz *Registrar) GetMiddlewares() []core.IMiddleware {
	return thiz.middlewares
}

func (thiz *Registrar) RegisterErrorHandler(errorType string, handler core.ErrorHandler) {
	if thiz.customErrorHandlers == nil {
		thiz.customErrorHandlers = make(map[string]core.ErrorHandler)
	}
	thiz.customErrorHandlers[errorType] = handler
}

func (thiz *Registrar) GetErrorHandlers() map[string]core.ErrorHandler {
	return thiz.customErrorHandlers
}

func (thiz *Registrar) RegisterLogger(key string, logger core.Logger) {
	if thiz.loggers == nil {
		thiz.loggers = make(map[string]core.Logger)
	}
	thiz.loggers[key] = logger
}

func (thiz *Registrar) GetLoggers() map[string]core.Logger {
	return thiz.loggers
}

func (thiz *Registrar) GetLogger(key string) core.Logger {
	return thiz.loggers[key]
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

func (thiz *Registrar) RegisterMigration(migration core.IMigration) {
	thiz.migrations = append(thiz.migrations, migration)
}

func (thiz *Registrar) GetMigrations() []core.IMigration {
	return thiz.migrations
}

func (thiz *Registrar) RegisterSeeder(seeder core.ISeeder) {
	thiz.seeders = append(thiz.seeders, seeder)
}

func (thiz *Registrar) GetSeeders() []core.ISeeder {
	return thiz.seeders
}

func (thiz *Registrar) SetSessionStorage(sessionStorage core.ISessionStorage) {
	thiz.sessionStorage = sessionStorage
}

func (thiz *Registrar) GetSessionStorage() core.ISessionStorage {
	return thiz.sessionStorage
}

func (thiz *Registrar) SetI18nManager(manager core.Ii18nManager) {
	thiz.i18nManager = manager
}

func (thiz *Registrar) GetI18nManager() core.Ii18nManager {
	return thiz.i18nManager
}

func (thiz *Registrar) RegisterDbConnection(kind core.DbType, connection core.IConnection) {
	thiz.dbConnections[kind] = connection
}

func (thiz *Registrar) GetDbConnections() map[core.DbType]core.IConnection {
	return thiz.dbConnections
}

func (thiz *Registrar) GetDbConnection(kind core.DbType) core.IConnection {
	return thiz.dbConnections[kind]
}

func (thiz *Registrar) RegisterViewEngine(engine core.IViewEngine) {
	thiz.viewEngine = engine
}

func (thiz *Registrar) GetViewEngine() core.IViewEngine {
	return thiz.viewEngine
}

func (thiz *Registrar) RegisterRouter(router core.Router) {
	thiz.router = router
}

func (thiz *Registrar) GetRouter() core.Router {
	return thiz.router
}

func (thiz *Registrar) RegisterContainer(container core.IContainer) {
	thiz.container = container
}

func (thiz *Registrar) GetContainer() core.IContainer {
	return thiz.container
}

func (thiz *Registrar) RegisterEventBus(eventBus core.IEventBus) {
	thiz.eventBus = eventBus
}

func (thiz *Registrar) GetEventBus() core.IEventBus {
	return thiz.eventBus
}
