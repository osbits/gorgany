package command

import (
	"gorgany/internal"
	"gorgany/proxy"
	"log"
)

type Resolver struct {
}

func NewCommandResolver() *Resolver {
	return &Resolver{}
}

func (thiz Resolver) ResolveCommand(commandName string) proxy.ICommand {
	command := internal.GetFrameworkRegistrar().GetCommand(commandName)
	if command == nil {
		log.Panicf("Command %s does not exist", commandName)
	}
	return command
}
