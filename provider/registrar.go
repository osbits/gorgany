package provider

import (
	"graecoFramework/command"
	"graecoFramework/db"
	"graecoFramework/http"
	"log"
)

var FrameworkRegistrar *Registrar

type Registrar struct {
	controllers http.Controllers
	providers   IProviders
	dbConfigs   map[db.Type]map[string]any // dbConfigs = { "postgres": {"host": "localhost", "port": "5432"...}, "mongo": {"host": "localhost"}
	commands    command.ICommands
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
