package provider

import (
	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/command"
	"github.com/osbits/gorgany/command/db"
	"github.com/osbits/gorgany/command/domain"
	"github.com/osbits/gorgany/err"
)

type CommandProvider struct {
	commands []core.ICommand
}

func NewCommandProvider() *CommandProvider {
	return &CommandProvider{
		commands: []core.ICommand{
			&command.VersionCommand{},
			&db.DiffCommand{},
			&db.MigrateCommand{},
			&db.SeedCommand{},
			&domain.RegisterDomainsCommand{},
		},
	}
}

func (thiz *CommandProvider) AddCommand(cmd core.ICommand) {
	thiz.commands = append(thiz.commands, cmd)
}

func (thiz *CommandProvider) Register(container core.IContainer) {
	container.SingletonLazy(func() core.IConsoleContext {
		return &command.ConsoleContext{}
	})
}

func (thiz *CommandProvider) Boot(container core.IContainer) {
	container.Invoke(func(consoleContext core.IConsoleContext) {
		for _, c := range thiz.commands {
			if e := container.Make(c); e != nil {
				err.HandleError(e)
				return
			}
			consoleContext.RegisterCommand(c)
		}
	})
}
