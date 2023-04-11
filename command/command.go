package command

var Commands map[string]ICommand

type ICommand interface {
	Execute()
	GetName() string
	GetSignature() string
}

type ICommands []ICommand
