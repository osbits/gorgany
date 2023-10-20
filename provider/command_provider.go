package provider

import (
	"gorgany/app/core"
	"gorgany/command"
	"gorgany/command/db"
	"gorgany/command/domain"
	"gorgany/internal"
)

type CommandProvider struct{}

func NewCommandProvider() *CommandProvider {
	return &CommandProvider{}
}

func (thiz *CommandProvider) InitProvider() {
	thiz.RegisterCommand(command.VersionCommand{})
	thiz.RegisterCommand(db.DiffCommand{})
	thiz.RegisterCommand(db.MigrateCommand{})
	thiz.RegisterCommand(db.SeedCommand{})
	thiz.RegisterCommand(domain.RegisterDomainsCommand{})
}

func (thiz *CommandProvider) RegisterCommand(cmd core.ICommand) {
	internal.GetFrameworkRegistrar().RegisterCommand(cmd)
}
