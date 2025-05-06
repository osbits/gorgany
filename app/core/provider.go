package core

type Bootstrapper interface {
	Bootstrap(container IContainer)
}

type IProvider interface {
	Register(container IContainer)
	Boot(container IContainer)
}
