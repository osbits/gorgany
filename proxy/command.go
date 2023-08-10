package proxy

type ICommand interface {
	Execute()
	GetName() string
}

type ICommands []ICommand
