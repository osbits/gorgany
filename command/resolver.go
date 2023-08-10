package command

import (
	"gorgany/proxy"
	"log"
)

type Resolver struct {
}

func NewCommandResolver() *Resolver {
	return &Resolver{}
}

func (thiz Resolver) ResolveCommand(commandName string) proxy.ICommand {
	command, ok := Commands[commandName]
	if !ok {
		log.Panicf("Command %s does not exist", commandName)
	}
	return command
}
