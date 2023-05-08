package command

var Commands map[string]ICommand

type ICommand interface {
	Execute()
	GetName() string
}

type ICommands []ICommand
