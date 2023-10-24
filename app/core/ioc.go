package core

type IContainer interface {
	Reset()
	Singleton(resolver interface{}) error
	SingletonLazy(resolver interface{}) error
	NamedSingleton(name string, resolver interface{}) error
	NamedSingletonLazy(name string, resolver interface{}) error
	Transient(resolver interface{}) error
	TransientLazy(resolver interface{}) error
	NamedTransient(name string, resolver interface{}) error
	NamedTransientLazy(name string, resolver interface{}) error
	Call(function interface{}) error
	Resolve(abstraction interface{}) error
	NamedResolve(abstraction interface{}, name string) error
	Make(structure interface{}, values ...map[string]interface{}) error
}

type Initiator interface {
	Init()
}
