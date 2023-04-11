package command

import "log"

type Resolver struct {
}

func NewCommandResolver() *Resolver {
	return &Resolver{}
}

func (thiz Resolver) ResolveCommand(commandName string) ICommand {
	command, ok := Commands[commandName]
	if !ok {
		log.Panicf("Command %s does not exist", commandName)
	}
	return command
}
