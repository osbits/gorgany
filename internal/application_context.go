package internal

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
)

var applicationContext core.IApplicationContext

func GetApplicationContext() core.IApplicationContext {
	return applicationContext
}

func InitApplicationContext() core.IApplicationContext {
	if applicationContext == nil {
		applicationContext = &ApplicationContext{
			AbstractApplicationContext: newAbstractContext(),
		}
	}
	return applicationContext
}

type ApplicationContext struct {
	*AbstractApplicationContext
}

func newAbstractContext() *AbstractApplicationContext {
	return &AbstractApplicationContext{
		controllers:         make(core.Controllers, 0),
		providers:           make(core.IProviders, 0),
		dbConnections:       make(map[string]core.GrgDBConnection),
		commands:            make(map[string]core.ICommand),
		middlewares:         make([]core.IMiddleware, 0),
		customErrorHandlers: make(map[string]core.ErrorHandler),
		loggers:             make(map[string]core.Logger),
		domains:             make(map[string]interface{}),
		migrations:          make([]core.IMigration, 0),
		seeders:             make([]core.ISeeder, 0),
		authStrategies:      make(map[string]core.IAuthStrategy),
	}
}

type AbstractApplicationContext struct {
	homeUrl             string
	controllers         core.Controllers
	providers           core.IProviders
	dbConnections       map[string]core.GrgDBConnection
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
	authStrategies      map[string]core.IAuthStrategy
	i18nManager         core.Ii18nManager
	viewEngine          core.IViewEngine
	router              core.Router
	container           core.IContainer
	eventBus            core.IEventBus
}

func (thiz *AbstractApplicationContext) SetHomeUrl(url string) {
	thiz.homeUrl = url
}

func (thiz *AbstractApplicationContext) GetHomeUrl() string {
	return thiz.homeUrl
}

func (thiz *AbstractApplicationContext) RegisterController(controller core.IController) {
	thiz.controllers = append(thiz.controllers, controller)
}

func (thiz *AbstractApplicationContext) GetControllers() core.Controllers {
	return thiz.controllers
}

func (thiz *AbstractApplicationContext) RegisterProvider(provider core.IProvider) {
	thiz.providers = append(thiz.providers, provider)
}

func (thiz *AbstractApplicationContext) GetProviders() core.IProviders {
	return thiz.providers
}

func (thiz *AbstractApplicationContext) RegisterCommand(command core.ICommand) {
	if thiz.commands == nil {
		thiz.commands = make(map[string]core.ICommand)
	}

	thiz.commands[command.GetName()] = command
}

func (thiz *AbstractApplicationContext) GetCommands() map[string]core.ICommand {
	return thiz.commands
}

func (thiz *AbstractApplicationContext) GetCommand(name string) core.ICommand {
	return thiz.commands[name]
}

func (thiz *AbstractApplicationContext) SetSessionLifetime(lifetime int) {
	thiz.sessionLifetime = lifetime
}

func (thiz *AbstractApplicationContext) GetSessionLifetime() int {
	if thiz.sessionLifetime == 0 {
		return 3600
	}
	return thiz.sessionLifetime
}

func (thiz *AbstractApplicationContext) SetUserService(service core.IUserService) {
	thiz.userService = service
}

func (thiz *AbstractApplicationContext) GetUserService() core.IUserService {
	return thiz.userService
}

func (thiz *AbstractApplicationContext) RegisterMiddleware(middleware core.IMiddleware) {
	if thiz.middlewares == nil {
		thiz.middlewares = make([]core.IMiddleware, 0)
	}
	thiz.middlewares = append(thiz.middlewares, middleware)
}

func (thiz *AbstractApplicationContext) GetMiddlewares() []core.IMiddleware {
	return thiz.middlewares
}

func (thiz *AbstractApplicationContext) RegisterErrorHandler(errorType string, handler core.ErrorHandler) {
	if thiz.customErrorHandlers == nil {
		thiz.customErrorHandlers = make(map[string]core.ErrorHandler)
	}
	thiz.customErrorHandlers[errorType] = handler
}

func (thiz *AbstractApplicationContext) GetErrorHandlers() map[string]core.ErrorHandler {
	return thiz.customErrorHandlers
}

func (thiz *AbstractApplicationContext) RegisterLogger(key string, logger core.Logger) {
	if thiz.loggers == nil {
		thiz.loggers = make(map[string]core.Logger)
	}
	thiz.loggers[key] = logger
}

func (thiz *AbstractApplicationContext) GetLoggers() map[string]core.Logger {
	return thiz.loggers
}

func (thiz *AbstractApplicationContext) GetLogger(key string) core.Logger {
	return thiz.loggers[key]
}

func (thiz *AbstractApplicationContext) RegisterDomain(key string, domain interface{}) {
	if thiz.domains == nil {
		thiz.domains = make(map[string]interface{})
	}
	thiz.domains[key] = domain
}

func (thiz *AbstractApplicationContext) GetDomains() map[string]interface{} {
	return thiz.domains
}

func (thiz *AbstractApplicationContext) RegisterMigration(migration core.IMigration) {
	thiz.migrations = append(thiz.migrations, migration)
}

func (thiz *AbstractApplicationContext) GetMigrations() []core.IMigration {
	return thiz.migrations
}

func (thiz *AbstractApplicationContext) RegisterSeeder(seeder core.ISeeder) {
	thiz.seeders = append(thiz.seeders, seeder)
}

func (thiz *AbstractApplicationContext) GetSeeders() []core.ISeeder {
	return thiz.seeders
}

func (thiz *AbstractApplicationContext) SetSessionStorage(sessionStorage core.ISessionStorage) {
	thiz.sessionStorage = sessionStorage
}

func (thiz *AbstractApplicationContext) GetSessionStorage() core.ISessionStorage {
	return thiz.sessionStorage
}

func (thiz *AbstractApplicationContext) SetAuthStrategy(authProvider core.IAuthStrategy, strategyName ...string) {
	name := core.DefaultKeyInRegistrar
	if len(strategyName) > 0 {
		name = strategyName[0]
	}

	thiz.authStrategies[name] = authProvider
}

func (thiz *AbstractApplicationContext) GetAuthStrategy(strategyName ...string) core.IAuthStrategy {
	name := core.DefaultKeyInRegistrar
	if len(strategyName) > 0 {
		name = strategyName[0]
	}

	return thiz.authStrategies[name]
}

func (thiz *AbstractApplicationContext) GetAuthStrategies() map[string]core.IAuthStrategy {
	return thiz.authStrategies
}

func (thiz *AbstractApplicationContext) SetI18nManager(manager core.Ii18nManager) {
	thiz.i18nManager = manager
}

func (thiz *AbstractApplicationContext) GetI18nManager() core.Ii18nManager {
	return thiz.i18nManager
}

func (thiz *AbstractApplicationContext) RegisterDbConnection(name string, connection core.GrgDBConnection) {
	thiz.dbConnections[name] = connection
}

func (thiz *AbstractApplicationContext) GetDbConnections() map[string]core.GrgDBConnection {
	return thiz.dbConnections
}

func (thiz *AbstractApplicationContext) GetDbConnection(name string) core.GrgDBConnection {
	return thiz.dbConnections[name]
}

func (thiz *AbstractApplicationContext) RegisterViewEngine(engine core.IViewEngine) {
	thiz.viewEngine = engine
}

func (thiz *AbstractApplicationContext) GetViewEngine() core.IViewEngine {
	return thiz.viewEngine
}

func (thiz *AbstractApplicationContext) RegisterRouter(router core.Router) {
	thiz.router = router
}

func (thiz *AbstractApplicationContext) GetRouter() core.Router {
	return thiz.router
}

func (thiz *AbstractApplicationContext) RegisterContainer(container core.IContainer) {
	thiz.container = container
}

func (thiz *AbstractApplicationContext) GetContainer() core.IContainer {
	return thiz.container
}

func (thiz *AbstractApplicationContext) RegisterEventBus(eventBus core.IEventBus) {
	thiz.eventBus = eventBus
}

func (thiz *AbstractApplicationContext) GetEventBus() core.IEventBus {
	return thiz.eventBus
}
