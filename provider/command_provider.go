package provider

import (
	"gorgany/command"
)

type CommandProvider struct{}

func NewCommandProvider() *CommandProvider {
	return &CommandProvider{}
}

func (thiz *CommandProvider) InitProvider() {
	command.Commands = make(map[string]command.ICommand)

	versionCommand := command.VersionCommand{}
	command.Commands[versionCommand.GetName()] = versionCommand

	for _, c := range FrameworkRegistrar.GetCommands() {
		command.Commands[c.GetName()] = c
	}
}
