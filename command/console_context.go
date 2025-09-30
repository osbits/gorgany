package command

import "github.com/gorganyio/gorgany/app/core"

type ConsoleContext struct {
	commands map[string]core.ICommand
}

func (thiz *ConsoleContext) Init() {
	thiz.commands = make(map[string]core.ICommand)
}

func (thiz *ConsoleContext) RegisterCommand(command core.ICommand) {
	if thiz.commands == nil {
		thiz.commands = make(map[string]core.ICommand)
	}
	thiz.commands[command.GetName()] = command
}

func (thiz *ConsoleContext) GetCommand(name string) core.ICommand {
	return thiz.commands[name]
}
