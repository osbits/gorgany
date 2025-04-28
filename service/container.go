package service

import (
	"errors"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"reflect"
	"sync"
)

// GetContainer returns the current IoC container for the application.
func GetContainer() core.IContainer {
	return internal.GetApplicationContext().GetContainer()
}

// binding holds a resolver function and a cached concrete instance for singletons.
type binding struct {
	resolver    interface{} // The function that creates the concrete instance.
	concrete    interface{} // Cached instance if this is a singleton.
	isSingleton bool        // True if the binding is a singleton.
}

// make resolves the binding. If this binding is a singleton and the concrete instance
// is already created, it returns the cached instance; otherwise, it calls the resolver.
// No container locks are held when calling the resolver.
func (b *binding) make(c *Container) (interface{}, error) {
	// Fast path: if singleton and instance exists, return it.
	if b.isSingleton && b.concrete != nil {
		return b.concrete, nil
	}

	// Otherwise, call the resolver to create an instance.
	retVal, err := c.invoke(b.resolver)
	if err != nil {
		return nil, err
	}
	if b.isSingleton {
		b.concrete = retVal
	}
	return retVal, nil
}

// Container is an IoC container that provides dependency registration and resolution.
// It is protected by a RWMutex for thread safety.
type Container struct {
	mu       sync.RWMutex
	bindings map[reflect.Type]map[string]*binding
}

// NewContainer creates a new Container.
func NewContainer() *Container {
	return &Container{
		bindings: make(map[reflect.Type]map[string]*binding),
	}
}

// bind registers a resolver with the given options (singleton/transient, lazy or not).
func (c *Container) bind(resolver interface{}, name string, isSingleton bool, isLazy bool) error {
	reflectedResolver := reflect.TypeOf(resolver)
	if reflectedResolver.Kind() != reflect.Func {
		return errors.New("container: the resolver must be a function")
	}

	// Resolver must return at least one value (the instance) and at most two (instance and error).
	if reflectedResolver.NumOut() == 0 || reflectedResolver.NumOut() > 2 {
		return errors.New("container: resolver function signature is invalid - it must return an instance, or instance and error")
	}
	resolveType := reflectedResolver.Out(0)

	// Validate the resolver signature.
	if err := c.validateResolverFunction(reflectedResolver); err != nil {
		return err
	}

	var concrete interface{}
	if !isLazy {
		var err error
		concrete, err = c.invoke(resolver)
		if err != nil {
			return err
		}
	}

	// Lock container for write to update bindings.
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exist := c.bindings[resolveType]; !exist {
		c.bindings[resolveType] = make(map[string]*binding)
	}
	c.bindings[resolveType][name] = &binding{
		resolver:    resolver,
		concrete:    concrete,
		isSingleton: isSingleton,
	}
	return nil
}

// validateResolverFunction checks that the resolver function has a valid signature.
func (c *Container) validateResolverFunction(funcType reflect.Type) error {
	retCount := funcType.NumOut()
	if retCount == 0 || retCount > 2 {
		return errors.New("container: resolver function signature is invalid")
	}
	resolveType := funcType.Out(0)
	// Ensure the function does not depend on the type it returns.
	for i := 0; i < funcType.NumIn(); i++ {
		if funcType.In(i) == resolveType {
			return fmt.Errorf("container: resolver function signature is invalid - dependency on the abstract it returns")
		}
	}
	return nil
}

// invoke calls the resolver function after resolving all its arguments.
// It then calls Make() on the created instance to inject its dependencies.
func (c *Container) invoke(function interface{}) (interface{}, error) {
	arguments, err := c.arguments(function)
	if err != nil {
		return nil, err
	}
	values := reflect.ValueOf(function).Call(arguments)
	// If the resolver returns an error as second value, check it.
	if len(values) == 2 && values[1].CanInterface() {
		if errVal, ok := values[1].Interface().(error); ok && errVal != nil {
			return values[0].Interface(), errVal
		}
	}
	instance := values[0].Interface()

	// Automatically inject dependencies into the created instance.
	err = c.Make(instance)
	if err != nil {
		return nil, err
	}
	return instance, nil
}

// arguments resolves and returns the list of arguments for the given function.
func (c *Container) arguments(function interface{}) ([]reflect.Value, error) {
	funcType := reflect.TypeOf(function)
	numIn := funcType.NumIn()
	args := make([]reflect.Value, numIn)
	for i := 0; i < numIn; i++ {
		abstraction := funcType.In(i)
		instance, err := c.resolveOrAutoRegister(abstraction, "")
		if err != nil {
			return nil, err
		}
		args[i] = reflect.ValueOf(instance)
	}
	return args, nil
}

// Reset clears all bindings in the container.
func (c *Container) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bindings = make(map[reflect.Type]map[string]*binding)
}

// ----------------------- Explicit Registration Methods ---------------------------

// Singleton registers a resolver as a singleton.
func (c *Container) Singleton(resolver interface{}) error {
	return c.bind(resolver, "", true, false)
}

// SingletonLazy registers a lazy singleton.
func (c *Container) SingletonLazy(resolver interface{}) error {
	return c.bind(resolver, "", true, true)
}

// NamedSingleton registers a named singleton.
func (c *Container) NamedSingleton(name string, resolver interface{}) error {
	return c.bind(resolver, name, true, false)
}

// NamedSingletonLazy registers a lazy named singleton.
func (c *Container) NamedSingletonLazy(name string, resolver interface{}) error {
	return c.bind(resolver, name, true, true)
}

// Bind registers a resolver as transient (a new instance is created on each call).
func (c *Container) Bind(resolver interface{}) error {
	return c.bind(resolver, "", false, false)
}

// BindLazy registers a lazy transient resolver.
func (c *Container) BindLazy(resolver interface{}) error {
	return c.bind(resolver, "", false, true)
}

// NamedBind registers a named transient resolver.
func (c *Container) NamedBind(name string, resolver interface{}) error {
	return c.bind(resolver, name, false, false)
}

// NamedBindLazy registers a lazy named transient resolver.
func (c *Container) NamedBindLazy(name string, resolver interface{}) error {
	return c.bind(resolver, name, false, true)
}

// -------------------- Dependency Resolution Methods -------------------------

// Resolve fills the provided abstract pointer with a singleton instance.
// If the service is not registered, it is automatically registered as a singleton.
// For resolving singletons, pass a double pointer, e.g.:
//
//	var svc *MyService
//	err := container.Resolve(&svc)
func (c *Container) Resolve(abstraction interface{}) error {
	return c.NamedResolve(abstraction, "")
}

// NamedResolve supports resolution of dependencies by name.
// If a double pointer is passed (e.g. **MyService), the container will resolve to a singleton
// (i.e. *MyService) and perform the appropriate assignment.
func (c *Container) NamedResolve(abstraction interface{}, name string) error {
	receiverType := reflect.TypeOf(abstraction)
	if receiverType == nil || receiverType.Kind() != reflect.Ptr {
		return errors.New("container: abstraction must be a pointer")
	}

	var targetType reflect.Type
	// If a double pointer is passed (e.g. **T), we resolve T.
	if receiverType.Elem().Kind() == reflect.Ptr {
		targetType = receiverType.Elem().Elem()
	} else {
		// Otherwise, we resolve the value pointed to.
		targetType = receiverType.Elem()
	}

	instance, err := c.resolveOrAutoRegister(targetType, name)
	if err != nil {
		return err
	}

	rv := reflect.ValueOf(abstraction).Elem()
	// If a double pointer is passed, instance is already a pointer.
	if receiverType.Elem().Kind() == reflect.Ptr {
		rv.Set(reflect.ValueOf(instance))
	} else {
		// Otherwise, assign the dereferenced value.
		rv.Set(reflect.ValueOf(instance).Elem())
	}
	return nil
}

// resolveOrAutoRegister attempts to find a binding for the given type and name.
// If no binding is found, it automatically registers a default singleton binding.
// For pointer types (e.g. *MyService), it creates an instance via reflect.New(t.Elem())
// so that the returned value is of type *MyService.
// The locking strategy ensures no locks are held while invoking external resolver code.
func (c *Container) resolveOrAutoRegister(t reflect.Type, name string) (interface{}, error) {
	// Try to find an existing binding with a read lock.
	c.mu.RLock()
	if bindingMap, exists := c.bindings[t]; exists {
		if b, found := bindingMap[name]; found {
			c.mu.RUnlock()
			return b.make(c)
		}
		// Fallback: try binding with the empty name.
		if b, found := bindingMap[""]; found {
			c.mu.RUnlock()
			return b.make(c)
		}
	}
	c.mu.RUnlock()

	// Define the default resolver that creates a new instance.
	defaultResolver := func() (interface{}, error) {
		var newInstance interface{}
		// For pointer types, create an instance of the element so we get *T.
		if t.Kind() == reflect.Ptr {
			newInstance = reflect.New(t.Elem()).Interface()
		} else {
			newInstance = reflect.New(t).Interface()
		}
		if initiator, ok := newInstance.(core.Initiator); ok {
			initiator.Init()
		}
		return newInstance, nil
	}

	// Acquire a write lock to auto-register the default binding.
	c.mu.Lock()
	bindingMap, exists := c.bindings[t]
	if !exists {
		bindingMap = make(map[string]*binding)
		c.bindings[t] = bindingMap
	}
	if b, found := bindingMap[name]; found {
		c.mu.Unlock()
		return b.make(c)
	}
	// Create a new binding with the default resolver.
	b := &binding{
		resolver:    defaultResolver,
		isSingleton: true,
	}
	bindingMap[name] = b
	// Release the lock before calling b.make to avoid deadlocks.
	c.mu.Unlock()

	return b.make(c)
}

// Make creates a new instance of the given structure and injects its dependencies.
// On the first call, if a dependency is not registered, it is auto-registered as a singleton.
// On subsequent calls for fields, a fresh instance is created if the service is transient.
func (c *Container) Make(structure interface{}, values ...map[string]interface{}) error {
	receiverType := reflect.TypeOf(structure)
	if receiverType == nil || receiverType.Kind() != reflect.Ptr {
		return errors.New("container: invalid structure - must be a pointer")
	}
	elem := receiverType.Elem()
	if elem.Kind() != reflect.Struct {
		return errors.New("container: invalid structure - must point to a struct")
	}
	s := reflect.ValueOf(structure).Elem()
	if len(values) > 0 {
		for fieldName, val := range values[0] {
			f := s.FieldByName(fieldName)
			if !f.IsValid() || !f.CanSet() {
				return fmt.Errorf("container: cannot set field %s", fieldName)
			}
			f.Set(reflect.ValueOf(val))
		}
	}
	// Start the recursive injection with an empty dependency chain.
	return c.fill(structure, make(map[reflect.Type]interface{}))
}

// fill recursively injects dependencies into fields tagged with `container:"inject"`.
// It tracks types in the current chain to detect cycles and reuses the top-most instance.
func (c *Container) fill(structure interface{}, chain map[reflect.Type]interface{}) error {
	// Must be pointer to struct
	vStruct := reflect.ValueOf(structure)
	if vStruct.Kind() != reflect.Ptr || vStruct.Elem().Kind() != reflect.Struct {
		return errors.New("container: structure must be a pointer to struct")
	}

	// Register this type in chain
	structType := reflect.TypeOf(structure) // e.g. *service.A
	chain[structType] = structure
	// Ensure we remove it when done
	defer delete(chain, structType)

	val := vStruct.Elem()
	rt := val.Type()

	for i := 0; i < val.NumField(); i++ {
		field := rt.Field(i)
		// skip anonymous fields (they are processed via recursive fill on their own)
		if field.Anonymous {
			fv := val.Field(i)
			// if pointer, simply call fill on it
			if fv.Kind() == reflect.Ptr && !fv.IsNil() {
				if err := c.fill(fv.Interface(), chain); err != nil {
					return err
				}
			}
			continue
		}

		// only fields tagged with `container:"inject"`
		if tag, ok := field.Tag.Lookup("container"); !ok || tag != "inject" {
			continue
		}

		fieldVal := val.Field(i)
		fieldType := field.Type // e.g. *service.B
		// cycle detected?
		if existing, inChain := chain[fieldType]; inChain {
			// reuse the already-being-constructed instance
			if !fieldVal.CanSet() {
				return fmt.Errorf("container: cannot set field %s", field.Name)
			}
			fieldVal.Set(reflect.ValueOf(existing))
			continue
		}

		// not in cycle, resolve or auto-register
		instance, err := c.resolveOrAutoRegister(fieldType, field.Name)
		if err != nil {
			return err
		}

		// recurse into its dependencies
		if err := c.fill(instance, chain); err != nil {
			return fmt.Errorf("container: cannot inject field %s: %w", field.Name, err)
		}

		// finally assign to struct field
		if !fieldVal.CanSet() {
			return fmt.Errorf("container: cannot set field %s", field.Name)
		}
		fieldVal.Set(reflect.ValueOf(instance))
	}

	// if implements core.Initiator, call Init()
	if initiator, ok := structure.(core.Initiator); ok {
		initiator.Init()
	}

	return nil
}
