package provider

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/command"
	"git.qix.sx/gorgany/gorgany.git/command/db"
	"git.qix.sx/gorgany/gorgany.git/command/domain"
	"git.qix.sx/gorgany/gorgany.git/internal"
)

type CommandProvider struct{}

func NewCommandProvider() *CommandProvider {
	return &CommandProvider{}
}

func (thiz *CommandProvider) InitProvider(appProvider core.IAppProvider) {
	thiz.RegisterCommand(command.VersionCommand{})
	thiz.RegisterCommand(db.DiffCommand{})
	thiz.RegisterCommand(db.MigrateCommand{})
	thiz.RegisterCommand(db.SeedCommand{})
	thiz.RegisterCommand(domain.RegisterDomainsCommand{})
}

func (thiz *CommandProvider) RegisterCommand(cmd core.ICommand) {
	internal.GetFrameworkRegistrar().RegisterCommand(cmd)
}
