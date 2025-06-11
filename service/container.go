package service

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unsafe"

	"git.qix.sx/gorgany/gorgany.git/app/core"
)

// binding holds resolver and cached instance for singletons
type binding struct {
	resolver    interface{}
	concrete    interface{}
	isSingleton bool

	initOnce *sync.Once
}

// Container is the IoC container implementation
type Container struct {
	mu          sync.RWMutex
	bindings    map[reflect.Type]map[string]*binding
	initMu      sync.Mutex
	initOnceMap map[interface{}]*sync.Once
}

// NewContainer creates a new Container
func NewContainer() *Container {
	return &Container{
		bindings:    make(map[reflect.Type]map[string]*binding),
		initOnceMap: make(map[interface{}]*sync.Once),
	}
}

// Reset clears all registrations
func (c *Container) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bindings = make(map[reflect.Type]map[string]*binding)
	c.initMu.Lock()
	defer c.initMu.Unlock()
	c.initOnceMap = make(map[interface{}]*sync.Once)
}

// Public registration methods
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
func (c *Container) Transient(resolver interface{}) error { return c.bind(resolver, "", false, false) }
func (c *Container) TransientLazy(resolver interface{}) error {
	return c.bind(resolver, "", false, true)
}
func (c *Container) NamedTransient(name string, resolver interface{}) error {
	return c.bind(resolver, name, false, false)
}
func (c *Container) NamedTransientLazy(name string, resolver interface{}) error {
	return c.bind(resolver, name, false, true)
}

// bind registers a resolver
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
	instType := fnType.Out(0)
	if instType.Kind() != reflect.Ptr && instType.Kind() != reflect.Interface {
		instType = reflect.PtrTo(instType)
	}
	var preInst interface{}
	if !isLazy {
		inst, err := c.invoke(resolver, make(map[reflect.Type]interface{}))
		if err != nil {
			return err
		}
		preInst = inst
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.bindings[instType]; !ok {
		c.bindings[instType] = make(map[string]*binding)
	}
	c.bindings[instType][name] = &binding{resolver: resolver, concrete: preInst, isSingleton: isSingleton}
	return nil
}

func (c *Container) validateResolver(fnType reflect.Type) error {
	resType := fnType.Out(0)
	for i := 0; i < fnType.NumIn(); i++ {
		if fnType.In(i) == resType {
			return fmt.Errorf("container: resolver cannot depend on its own return type %s", resType)
		}
	}
	return nil
}

// Make injects fields into ptr-to-struct or ptr-to-interface
func (c *Container) Make(target interface{}, overrides ...map[string]interface{}) error {
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Ptr {
		return errors.New("container: Make requires a pointer")
	}
	if v.Elem().Kind() == reflect.Struct {
		if len(overrides) > 0 {
			s := v.Elem()
			for field, val := range overrides[0] {
				f := s.FieldByName(field)
				if f.IsValid() {
					ptr := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
					if ptr.IsValid() && ptr.CanSet() {
						ptr.Set(reflect.ValueOf(val))
					}
				}
			}
		}
		return c.fill(v.Interface(), make(map[reflect.Type]interface{}))
	}
	// interface pointer: map to resolveInternal
	return c.namedResolveInternal(target, "")
}

func (c *Container) Resolve(abstraction interface{}) error {
	return c.namedResolveInternal(abstraction, "")
}

func (c *Container) NamedResolve(abstraction interface{}, name string) error {
	return c.namedResolveInternal(abstraction, name)
}

func (c *Container) Invoke(fn interface{}) error {
	if reflect.TypeOf(fn).Kind() != reflect.Func {
		return errors.New("container: invalid function")
	}
	_, err := c.invoke(fn, make(map[reflect.Type]interface{}))
	return err
}

func (c *Container) namedResolveInternal(abstraction interface{}, name string) error {
	rv := reflect.ValueOf(abstraction)
	if rv.Kind() != reflect.Ptr || rv.Elem().Kind() == reflect.Struct {
		return errors.New("container: abstraction must be pointer to interface")
	}
	et := rv.Elem().Type()
	inst, err := c.resolve(et, name, make(map[reflect.Type]interface{}))
	if err != nil {
		return err
	}
	iv := reflect.ValueOf(inst)
	if !iv.Type().AssignableTo(et) {
		return fmt.Errorf("container: cannot assign %s to %s", iv.Type(), et)
	}
	rv.Elem().Set(iv)
	return nil
}

func (c *Container) resolve(t reflect.Type, name string, chain map[reflect.Type]interface{}) (interface{}, error) {
	// First try direct type resolution
	c.mu.RLock()
	if m, ok := c.bindings[t]; ok {
		if b, found := m[name]; found {
			c.mu.RUnlock()
			return c.getInstance(b, chain)
		}
		if b, found := m[""]; found {
			c.mu.RUnlock()
			return c.getInstance(b, chain)
		}
	}
	c.mu.RUnlock()

	// interface: find concrete binding that implements t
	if t.Kind() == reflect.Interface {
		c.mu.RLock()
		bindings := make([]*binding, 0)
		// Collect all potential bindings first to minimize lock time
		for keyType, m := range c.bindings {
			if keyType.Implements(t) || (keyType.Kind() == reflect.Ptr && keyType.Elem().Implements(t)) {
				if b, found := m[name]; found {
					bindings = append(bindings, b)
				}
				if b, found := m[""]; found {
					bindings = append(bindings, b)
				}
			}
		}
		c.mu.RUnlock()

		// Try to get instance from collected bindings
		for _, b := range bindings {
			inst, err := c.getInstance(b, chain)
			if err == nil {
				return inst, nil
			}
		}
	}

	// pointer-to-struct auto-register
	if t.Kind() == reflect.Ptr && t.Elem().Kind() == reflect.Struct {
		c.mu.Lock()
		if _, ok := c.bindings[t]; !ok {
			defaultResolver := func() (interface{}, error) {
				return reflect.New(t.Elem()).Interface(), nil
			}
			c.bindings[t] = map[string]*binding{"": {resolver: defaultResolver, isSingleton: true}}
		}
		c.mu.Unlock()
		return c.resolve(t, name, chain)
	}
	return nil, fmt.Errorf("container: no binding for type %s", t)
}

func (c *Container) getInstance(b *binding, chain map[reflect.Type]interface{}) (interface{}, error) {
	if b.isSingleton && b.concrete != nil {
		return b.concrete, nil
	}

	if !b.isSingleton {
		return c.invoke(b.resolver, chain)
	}

	c.mu.RLock()
	if b.concrete != nil {
		instance := b.concrete
		c.mu.RUnlock()
		return instance, nil
	}
	c.mu.RUnlock()

	inst, err := c.invoke(b.resolver, chain)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if b.concrete == nil {
		b.concrete = inst
	} else {
		inst = b.concrete
	}
	c.mu.Unlock()

	return inst, nil
}

func (c *Container) invoke(fn interface{}, chain map[reflect.Type]interface{}) (interface{}, error) {
	args, err := c.buildArgs(fn, chain)
	if err != nil {
		return nil, err
	}
	results := reflect.ValueOf(fn).Call(args)
	if results == nil {
		return nil, nil
	}

	inst := results[0].Interface()

	for i := 0; i < len(results); i++ {
		if results[i].IsZero() {
			continue
		}
		if e, ok := results[i].Interface().(error); ok && e != nil {
			return inst, e
		}
	}

	if err := c.fill(inst, chain); err != nil {
		return inst, err
	}
	return inst, nil
}

func (c *Container) buildArgs(fn interface{}, chain map[reflect.Type]interface{}) ([]reflect.Value, error) {
	fnType := reflect.TypeOf(fn)
	args := make([]reflect.Value, fnType.NumIn())
	for i := 0; i < fnType.NumIn(); i++ {
		dep := fnType.In(i)
		if dep.Kind() != reflect.Interface {
			if dep.Kind() != reflect.Ptr {
				dep = reflect.PtrTo(dep)
			}
		}
		inst, err := c.resolve(dep, "", chain)
		if err != nil {
			return nil, err
		}
		args[i] = reflect.ValueOf(inst)
	}
	return args, nil
}

func (c *Container) fill(target interface{}, chain map[reflect.Type]interface{}) error {
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	tptr := reflect.TypeOf(target)
	if _, seen := chain[tptr]; seen {
		return nil
	}
	chain[tptr] = target
	defer delete(chain, tptr)

	s := v.Elem()
	rt := s.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if field.Anonymous {
			fType := field.Type
			if fType.Kind() == reflect.Ptr {
				if err := c.fill(s.Field(i).Interface(), chain); err != nil {
					return err
				}
			} else if fType.Kind() == reflect.Struct {
				ptr := reflect.NewAt(fType, unsafe.Pointer(s.Field(i).UnsafeAddr()))
				if err := c.fill(ptr.Interface(), chain); err != nil {
					return err
				}
			}
			continue
		}

		// Parse container tag
		tag, ok := field.Tag.Lookup("container")
		if !ok {
			continue
		}

		// Parse tag options
		var injectName string
		if tag != "inject" {
			// Check for named injection
			if strings.HasPrefix(tag, "inject:") {
				injectName = strings.TrimPrefix(tag, "inject:")
			} else {
				continue
			}
		}

		fv := s.Field(i)
		ftype := field.Type
		var keyType reflect.Type
		switch ftype.Kind() {
		case reflect.Ptr:
			keyType = ftype
		case reflect.Struct:
			keyType = reflect.PtrTo(ftype)
		case reflect.Interface:
			keyType = ftype
		default:
			continue
		}

		inst, err := c.resolve(keyType, injectName, chain)
		if err != nil {
			return err
		}

		if err := c.fill(inst, chain); err != nil {
			return err
		}

		val := reflect.ValueOf(inst)
		if ftype.Kind() == reflect.Interface {
			ptr := unsafe.Pointer(fv.UnsafeAddr())
			mutable := reflect.NewAt(ftype, ptr).Elem()
			if mutable.IsValid() && mutable.CanSet() {
				mutable.Set(val)
			}
		} else {
			var toSet reflect.Value
			if ftype.Kind() == reflect.Ptr {
				toSet = val
			} else {
				toSet = val.Elem()
			}
			if fv.CanSet() {
				fv.Set(toSet)
			} else {
				ptr := reflect.NewAt(ftype, unsafe.Pointer(fv.UnsafeAddr())).Elem()
				if ptr.IsValid() && ptr.CanSet() {
					ptr.Set(toSet)
				}
			}
		}
	}

	if initObj, ok := target.(core.Initiator); ok {
		c.initMu.Lock()
		initOnce, exists := c.initOnceMap[target]
		if !exists {
			initOnce = &sync.Once{}
			c.initOnceMap[target] = initOnce
		}
		c.initMu.Unlock()

		initOnce.Do(func() {
			initObj.Init()
		})
	}

	return nil
}
