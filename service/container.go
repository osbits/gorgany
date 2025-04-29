package service

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"unsafe"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/internal"
)

// GetContainer returns the application's IoC container.
func GetContainer() core.IContainer {
	return internal.GetApplicationContext().GetContainer()
}

// binding holds a resolver and, for singletons, the cached instance.
type binding struct {
	resolver    interface{}
	concrete    interface{}
	isSingleton bool
}

// Container is the IoC container implementation.
type Container struct {
	mu          sync.RWMutex
	bindings    map[reflect.Type]map[string]*binding
	initMu      sync.Mutex
	initialized map[uintptr]bool // tracks which instances had Init called
}

// NewContainer creates a new Container.
func NewContainer() *Container {
	return &Container{
		bindings:    make(map[reflect.Type]map[string]*binding),
		initialized: make(map[uintptr]bool),
	}
}

// Reset clears all registered bindings and initialization state.
func (c *Container) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bindings = make(map[reflect.Type]map[string]*binding)
	c.initMu.Lock()
	defer c.initMu.Unlock()
	c.initialized = make(map[uintptr]bool)
}

// Public registration methods (all default to singleton):
func (c *Container) Singleton(resolver interface{}) error { return c.bind(resolver, "", true, false) }
func (c *Container) SingletonLazy(resolver interface{}) error {
	return c.bind(resolver, "", true, true)
}
func (c *Container) NamedSingleton(name string, resolver interface{}) error {
	return c.bind(resolver, name, true, false)
}
func (c *Container) NamedSingletonLazy(name string, resolver interface{}) error {
	return c.bind(resolver, name, true, true)
}
func (c *Container) Bind(resolver interface{}) error     { return c.bind(resolver, "", true, false) }
func (c *Container) BindLazy(resolver interface{}) error { return c.bind(resolver, "", true, true) }
func (c *Container) NamedBind(name string, resolver interface{}) error {
	return c.bind(resolver, name, true, false)
}
func (c *Container) NamedBindLazy(name string, resolver interface{}) error {
	return c.bind(resolver, name, true, true)
}

// bind registers a resolver with the given options.
func (c *Container) bind(resolver interface{}, name string, isSingleton, isLazy bool) error {
	fnType := reflect.TypeOf(resolver)
	if fnType.Kind() != reflect.Func {
		return errors.New("container: resolver must be a function")
	}
	if fnType.NumOut() == 0 || fnType.NumOut() > 2 {
		return errors.New("container: resolver must return (instance [, error])")
	}
	if err := c.validateResolver(fnType); err != nil {
		return err
	}

	// Normalize to pointer type for storage.
	instType := fnType.Out(0)
	if instType.Kind() != reflect.Ptr {
		instType = reflect.PtrTo(instType)
	}

	// Pre-instantiate non-lazy singletons via internal invoke.
	var preInst interface{}
	if !isLazy {
		inst, err := c.invokeChain(resolver, make(map[reflect.Type]interface{}))
		if err != nil {
			return err
		}
		preInst = inst
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.bindings[instType]; !exists {
		c.bindings[instType] = make(map[string]*binding)
	}
	c.bindings[instType][name] = &binding{resolver: resolver, concrete: preInst, isSingleton: isSingleton}
	return nil
}

// validateResolver ensures resolver function does not depend on its own return type.
func (c *Container) validateResolver(fnType reflect.Type) error {
	resType := fnType.Out(0)
	for i := 0; i < fnType.NumIn(); i++ {
		if fnType.In(i) == resType {
			return fmt.Errorf("container: resolver cannot depend on its own return type %s", resType)
		}
	}
	return nil
}

// Public proxy methods for internal logic:
func (c *Container) Make(target interface{}, overrides ...map[string]interface{}) error {
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return errors.New("container: Make requires pointer to struct")
	}
	if len(overrides) > 0 {
		s := v.Elem()
		for field, val := range overrides[0] {
			f := s.FieldByName(field)
			if !f.IsValid() || !f.CanSet() {
				return fmt.Errorf("container: cannot set field %s", field)
			}
			f.Set(reflect.ValueOf(val))
		}
	}
	return c.fillInternal(target, make(map[reflect.Type]interface{}))
}

func (c *Container) Call(fn interface{}) error {
	_, err := c.invokeChain(fn, make(map[reflect.Type]interface{}))
	return err
}

func (c *Container) Resolve(abstraction interface{}) error {
	return c.namedResolveInternal(abstraction, "")
}

func (c *Container) NamedResolve(abstraction interface{}, name string) error {
	return c.namedResolveInternal(abstraction, name)
}

// namedResolveInternal resolves into pointer, using internal resolve logic.
func (c *Container) namedResolveInternal(abstraction interface{}, name string) error {
	rv := reflect.ValueOf(abstraction)
	if rv.Kind() != reflect.Ptr {
		return errors.New("container: abstraction must be a pointer")
	}
	dst := rv.Elem()
	et := dst.Type()
	key := reflect.PtrTo(et)
	inst, err := c.resolveInternal(key, name, make(map[reflect.Type]interface{}))
	if err != nil {
		return err
	}
	iv := reflect.ValueOf(inst)
	if !iv.Type().AssignableTo(dst.Type()) {
		if iv.Kind() == reflect.Ptr && iv.Elem().Type().AssignableTo(dst.Type()) {
			iv = iv.Elem()
		} else {
			return fmt.Errorf("container: cannot assign %s to %s", iv.Type(), dst.Type())
		}
	}
	dst.Set(iv)
	return nil
}

// resolveInternal resolves or auto-registers, with cycle detection via chain.
func (c *Container) resolveInternal(t reflect.Type, name string, chain map[reflect.Type]interface{}) (interface{}, error) {
	c.mu.RLock()
	if m, ok := c.bindings[t]; ok {
		if b, found := m[name]; found {
			c.mu.RUnlock()
			return b.makeChain(c, chain)
		}
		if b, found := m[""]; found {
			c.mu.RUnlock()
			return b.makeChain(c, chain)
		}
	}
	c.mu.RUnlock()

	// auto-register default singleton for pointer to struct
	if t.Kind() == reflect.Ptr && t.Elem().Kind() == reflect.Struct {
		c.mu.Lock()
		if _, exists := c.bindings[t]; !exists {
			c.bindings[t] = make(map[string]*binding)
		}
		defaultResolver := func() (interface{}, error) {
			inst := reflect.New(t.Elem()).Interface()
			return inst, nil
		}
		b := &binding{resolver: defaultResolver, isSingleton: true}
		c.bindings[t][""] = b
		c.mu.Unlock()
		return b.makeChain(c, chain)
	}

	return nil, fmt.Errorf("container: no binding found for type %s", t)
}

// binding.makeChain is like make but propagates chain.
func (b *binding) makeChain(c *Container, chain map[reflect.Type]interface{}) (interface{}, error) {
	if b.isSingleton && b.concrete != nil {
		return b.concrete, nil
	}
	inst, err := c.invokeChain(b.resolver, chain)
	if err != nil {
		return nil, err
	}
	if b.isSingleton {
		b.concrete = inst
	}
	return inst, nil
}

// invokeChain invokes resolver, injects its result, propagating chain.
func (c *Container) invokeChain(fn interface{}, chain map[reflect.Type]interface{}) (interface{}, error) {
	args, err := c.argumentsChain(fn, chain)
	if err != nil {
		return nil, err
	}
	results := reflect.ValueOf(fn).Call(args)
	inst := results[0].Interface()
	if len(results) == 2 {
		if e, ok := results[1].Interface().(error); ok && e != nil {
			return inst, e
		}
	}
	if err := c.fillInternal(inst, chain); err != nil {
		return inst, err
	}
	return inst, nil
}

// argumentsChain resolves function parameters via internal resolve.
func (c *Container) argumentsChain(fn interface{}, chain map[reflect.Type]interface{}) ([]reflect.Value, error) {
	fnType := reflect.TypeOf(fn)
	args := make([]reflect.Value, fnType.NumIn())
	for i := 0; i < fnType.NumIn(); i++ {
		dep := fnType.In(i)
		if dep.Kind() != reflect.Ptr {
			dep = reflect.PtrTo(dep)
		}
		inst, err := c.resolveInternal(dep, "", chain)
		if err != nil {
			return nil, err
		}
		args[i] = reflect.ValueOf(inst)
	}
	return args, nil
}

// fillInternal injects struct fields tagged `container:"inject"`, reusing on cycles.
func (c *Container) fillInternal(target interface{}, chain map[reflect.Type]interface{}) error {
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	tptr := reflect.TypeOf(target)
	if _, exists := chain[tptr]; exists {
		return nil
	}
	chain[tptr] = target
	defer delete(chain, tptr)

	s := v.Elem()
	rt := s.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if field.Anonymous {
			fv := s.Field(i)
			if fv.Kind() == reflect.Ptr && !fv.IsNil() {
				c.fillInternal(fv.Interface(), chain)
			}
			continue
		}
		tag, ok := field.Tag.Lookup("container")
		if !ok || tag != "inject" {
			continue
		}
		fv := s.Field(i)
		ftype := field.Type

		var keyType reflect.Type
		switch ftype.Kind() {
		case reflect.Ptr:
			keyType = ftype
		case reflect.Struct:
			keyType = reflect.PtrTo(ftype)
		default:
			continue
		}

		if existing, seen := chain[keyType]; seen {
			ev := reflect.ValueOf(existing)
			var toSet reflect.Value
			if ftype.Kind() == reflect.Ptr {
				toSet = ev
			} else {
				toSet = ev.Elem()
			}
			if fv.CanSet() {
				fv.Set(toSet)
			} else {
				ptr := reflect.NewAt(ftype, unsafe.Pointer(fv.UnsafeAddr()))
				ptr.Elem().Set(toSet)
			}
			continue
		}

		inst, err := c.resolveInternal(keyType, "", chain)
		if err != nil {
			return err
		}
		if err := c.fillInternal(inst, chain); err != nil {
			return fmt.Errorf("container: cannot inject field %s: %w", field.Name, err)
		}

		ev := reflect.ValueOf(inst)
		var toSet reflect.Value
		if ftype.Kind() == reflect.Ptr {
			toSet = ev
		} else {
			toSet = ev.Elem()
		}
		if fv.CanSet() {
			fv.Set(toSet)
		} else {
			ptr := reflect.NewAt(ftype, unsafe.Pointer(fv.UnsafeAddr()))
			ptr.Elem().Set(toSet)
		}
	}

	// call Init() once per instance
	if initObj, ok := target.(core.Initiator); ok {
		ptrVal := reflect.ValueOf(target)
		addr := ptrVal.Pointer()
		c.initMu.Lock()
		already := c.initialized[addr]
		if !already {
			initObj.Init()
			c.initialized[addr] = true
		}
		c.initMu.Unlock()
	}
	return nil
}
