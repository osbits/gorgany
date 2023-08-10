package provider

import (
	"gorgany/command"
	"gorgany/proxy"
)

type CommandProvider struct{}

func NewCommandProvider() *CommandProvider {
	return &CommandProvider{}
}

func (thiz *CommandProvider) InitProvider() {
	command.Commands = make(map[string]proxy.ICommand)

	versionCommand := command.VersionCommand{}
	command.Commands[versionCommand.GetName()] = versionCommand

	for _, c := range FrameworkRegistrar.GetCommands() {
		command.Commands[c.GetName()] = c
	}
}
