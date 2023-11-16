package core

type IProviders []IProvider

type IProvider interface {
	InitProvider()
}

type IAppProvider interface {
	IProvider
	RegisterProvider(provider IProvider)
}

type IRegistrar interface {
	SetHomeUrl(url string)
	GetHomeUrl() string
	RegisterController(controller IController)
	GetControllers() Controllers
	RegisterProvider(provider IProvider)
	GetProviders() IProviders
	RegisterDbConnection(name string, connection IConnection)
	GetDbConnections() map[string]IConnection
	GetDbConnection(name string) IConnection
	RegisterCommand(command ICommand)
	GetCommands() map[string]ICommand
	GetCommand(name string) ICommand
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
	GetLogger(key string) Logger
	RegisterDomain(key string, domain interface{})
	GetDomains() map[string]interface{}
	RegisterMigration(migration IMigration)
	GetMigrations() []IMigration
	RegisterSeeder(seeder ISeeder)
	GetSeeders() []ISeeder
	SetSessionStorage(sessionStorage ISessionStorage)
	GetSessionStorage() ISessionStorage
	SetI18nManager(manager Ii18nManager)
	GetI18nManager() Ii18nManager
	RegisterViewEngine(engine IViewEngine)
	GetViewEngine() IViewEngine
	RegisterRouter(router Router)
	GetRouter() Router
	RegisterContainer(container IContainer)
	GetContainer() IContainer
	RegisterEventBus(bus IEventBus)
	GetEventBus() IEventBus
}
