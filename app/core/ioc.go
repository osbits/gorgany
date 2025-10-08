package core

// IContainer defines the interface for dependency injection container
type IContainer interface {
	// Reset clears all registered dependencies
	Reset()

	// Singleton registers a singleton dependency that is resolved immediately
	Singleton(resolver interface{}) error
	// SingletonLazy registers a singleton dependency that is resolved lazily
	SingletonLazy(resolver interface{}) error
	// NamedSingleton registers a named singleton dependency that is resolved immediately
	NamedSingleton(name string, resolver interface{}) error
	// NamedSingletonLazy registers a named singleton dependency that is resolved lazily
	NamedSingletonLazy(name string, resolver interface{}) error

	// Transient registers a transient dependency that is resolved immediately
	Transient(resolver interface{}) error
	// TransientLazy registers a transient dependency that is resolved lazily
	TransientLazy(resolver interface{}) error
	// NamedTransient registers a named transient dependency that is resolved immediately
	NamedTransient(name string, resolver interface{}) error
	// NamedTransientLazy registers a named transient dependency that is resolved lazily
	NamedTransientLazy(name string, resolver interface{}) error

	// Invoke calls a function with its dependencies resolved
	Invoke(fn interface{}) error

	// Resolve resolves a dependency into the provided abstraction
	Resolve(abstraction interface{}) error
	// NamedResolve resolves a named dependency into the provided abstraction
	NamedResolve(abstraction interface{}, name string) error

	// Make creates a new instance of a structure with its dependencies resolved
	Make(structure interface{}, values ...map[string]interface{}) error
}

// IEmergencyContainer defines a minimal interface for emergency dependency resolution
type IEmergencyContainer interface {
	// Invoke calls a function with its dependencies resolved
	Invoke(fn interface{}) error
	// Resolve resolves a dependency into the provided abstraction
	Resolve(abstraction interface{}) error
	// NamedResolve resolves a named dependency into the provided abstraction
	NamedResolve(abstraction interface{}, name string) error
	// Make creates a new instance of a structure with its dependencies resolved
	Make(structure interface{}, values ...map[string]interface{}) error
}

// Initiator defines the interface for objects that require initialization
type Initiator interface {
	// Init performs initialization of the object
	Init()
}
