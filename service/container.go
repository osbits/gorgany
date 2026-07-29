package service

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unsafe"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/log"
	"github.com/spf13/viper"
)

var emergencyContainerFactory func() core.IEmergencyContainer

func SetEmergencyContainerFactory(factory func() core.IEmergencyContainer) {
	emergencyContainerFactory = factory
}

func EmergencyContainer() core.IEmergencyContainer {
	return emergencyContainerFactory()
}

// binding holds resolver and cached instance for singletons
type binding struct {
	resolver    interface{}
	concrete    interface{}
	isSingleton bool
	// explicit is true for a binding created by a Singleton*/Transient* call, and
	// false for one the container auto-registered while resolving a
	// pointer-to-struct. Make uses it to tell "the caller wanted the registered
	// instance" from "this type has merely been resolved before".
	explicit     bool
	mu           sync.Mutex
	cond         *sync.Cond
	constructing bool
}

// Container is the IoC container implementation
type Container struct {
	mu          sync.RWMutex
	bindings    map[reflect.Type]map[string]*binding
	initMu      sync.Mutex
	initOnceMap map[interface{}]*sync.Once
}

type dependencyKey struct {
	t    reflect.Type
	name string
}

type circularDependencyError struct {
	path string
}

func (e *circularDependencyError) Error() string {
	return e.path
}

type resolutionState struct {
	active map[dependencyKey]struct{}
	stack  []dependencyKey
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
		inst, err := c.invoke(resolver, newResolutionState())
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

	// Rebinding is intentionally still allowed: overwriting is currently the only
	// mechanism an app has to override a framework service such as
	// core.IValidator or core.IDataContext, and taking that away would break every
	// app that does it. But it used to happen with no signal at all, so a provider
	// registered in the wrong order silently won and the symptom showed up much
	// later as "my override isn't being used".
	//
	// Overriding a core interface now logs at warn. The supported pattern is to
	// register the override provider LAST.
	if _, replaced := c.bindings[instType][name]; replaced && isCoreAbstraction(instType) {
		log.Log().Warnf(
			"container: rebinding %s%s — the previous binding is discarded. "+
				"This is supported, but only the last registration wins, so register "+
				"your override provider last.",
			instType, namedSuffix(name))
	}

	c.bindings[instType][name] = &binding{
		resolver:    resolver,
		concrete:    preInst,
		isSingleton: isSingleton,
		explicit:    true,
	}
	return nil
}

// coreAbstractionPkg is the import path whose interfaces are the framework's
// extension points.
const coreAbstractionPkg = "github.com/osbits/gorgany/app/core"

// isCoreAbstraction reports whether t is one of the framework's core interfaces.
//
// The warning is scoped to those deliberately: an app rebinding its own service is
// ordinary, whereas replacing a framework contract is the case where registration
// order decides behaviour and silence costs debugging time.
func isCoreAbstraction(t reflect.Type) bool {
	if t.Kind() != reflect.Interface {
		return false
	}
	return t.PkgPath() == coreAbstractionPkg
}

func namedSuffix(name string) string {
	if name == "" {
		return ""
	}
	return fmt.Sprintf("[%s]", name)
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
		// A pointer-to-struct takes the field-injection branch below, which fills
		// the struct the caller already has. A caller who instead expected to
		// *receive* the registered instance gets a zero value and a nil error — that
		// silence is how JobProvider.Boot came to tick an empty &gocron.Scheduler{}
		// for as long as this has been shipping.
		//
		// The two intents are told apart by identity. Passing the very instance the
		// binding holds means "fill my fields", which is a supported pattern.
		// Passing a different instance of a type that *is* registered means the
		// caller wanted Resolve, so say so instead of returning a zero value.
		if targetType := reflect.TypeOf(target); c.hasForeignExplicitBinding(targetType, target) {
			return fmt.Errorf(
				"container: Make(%s) fills a struct's container:\"inject\" fields, but %s "+
					"has a registered binding and this is not the bound instance — use "+
					"Resolve(**%s) to obtain the registered one",
				targetType, targetType, targetType.Elem())
		}

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
		return c.fill(v.Interface(), newResolutionState())
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
	_, err := c.invoke(fn, newResolutionState())
	return err
}

func (c *Container) namedResolveInternal(abstraction interface{}, name string) error {
	rv := reflect.ValueOf(abstraction)
	if rv.Kind() != reflect.Ptr || rv.Elem().Kind() == reflect.Struct {
		return errors.New("container: abstraction must be pointer to interface")
	}
	et := rv.Elem().Type()
	inst, err := c.resolve(et, name, newResolutionState())
	if err != nil {
		return err
	}
	if inst == nil {
		rv.Elem().Set(reflect.Zero(et))
		return nil
	}
	iv := reflect.ValueOf(inst)
	if !iv.Type().AssignableTo(et) {
		return fmt.Errorf("container: cannot assign %s to %s", iv.Type(), et)
	}
	rv.Elem().Set(iv)
	return nil
}

func (c *Container) resolve(t reflect.Type, name string, state *resolutionState) (interface{}, error) {
	key := dependencyKey{t: t, name: name}
	if state != nil {
		if _, exists := state.active[key]; exists {
			return c.resolveCircularDependency(key, state)
		}
	}
	c.enterDependency(key, state)
	defer c.leaveDependency(key, state)

	// First try direct type resolution
	c.mu.RLock()
	if m, ok := c.bindings[t]; ok {
		if b, found := m[name]; found {
			c.mu.RUnlock()
			return c.getInstance(b, state)
		}
		if b, found := m[""]; found {
			c.mu.RUnlock()
			return c.getInstance(b, state)
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
			inst, err := c.getInstance(b, state)
			if err == nil {
				return inst, nil
			}
		}
	}

	// pointer-to-struct auto-register
	if t.Kind() == reflect.Ptr && t.Elem().Kind() == reflect.Struct {
		var autoBinding *binding
		c.mu.Lock()
		if _, ok := c.bindings[t]; !ok {
			defaultResolver := func() (interface{}, error) {
				return reflect.New(t.Elem()).Interface(), nil
			}
			c.bindings[t] = map[string]*binding{"": {resolver: defaultResolver, isSingleton: true}}
		}
		autoBinding = c.bindings[t][""]
		if autoBinding.cond == nil {
			autoBinding.cond = sync.NewCond(&autoBinding.mu)
		}
		c.mu.Unlock()
		return c.getInstance(autoBinding, state)
	}
	return nil, fmt.Errorf("container: no binding for type %s", t)
}

func (c *Container) getInstance(b *binding, state *resolutionState) (interface{}, error) {
	if b.isSingleton && b.concrete != nil {
		return b.concrete, nil
	}

	if !b.isSingleton {
		return c.invoke(b.resolver, state)
	}

	b.mu.Lock()
	if b.cond == nil {
		b.cond = sync.NewCond(&b.mu)
	}

	for b.constructing {
		b.cond.Wait()
		if b.concrete != nil {
			inst := b.concrete
			b.mu.Unlock()
			return inst, nil
		}
	}
	if b.concrete != nil {
		inst := b.concrete
		b.mu.Unlock()
		return inst, nil
	}
	b.constructing = true
	b.mu.Unlock()

	inst, err := c.callResolver(b.resolver, state)
	if err != nil {
		b.mu.Lock()
		b.constructing = false
		b.cond.Broadcast()
		b.mu.Unlock()
		return nil, err
	}

	b.mu.Lock()
	b.concrete = inst
	b.mu.Unlock()

	if err := c.fill(inst, state); err != nil {
		b.mu.Lock()
		b.concrete = nil
		b.constructing = false
		b.cond.Broadcast()
		b.mu.Unlock()
		return inst, err
	}

	b.mu.Lock()
	b.constructing = false
	b.cond.Broadcast()
	b.mu.Unlock()

	return inst, nil
}

func (c *Container) invoke(fn interface{}, state *resolutionState) (interface{}, error) {
	inst, err := c.callResolver(fn, state)
	if err != nil {
		return nil, err
	}

	if err := c.fill(inst, state); err != nil {
		return inst, err
	}
	return inst, nil
}

func (c *Container) callResolver(fn interface{}, state *resolutionState) (interface{}, error) {
	args, err := c.buildArgs(fn, state)
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

	return inst, nil
}

func (c *Container) buildArgs(fn interface{}, state *resolutionState) ([]reflect.Value, error) {
	fnType := reflect.TypeOf(fn)
	args := make([]reflect.Value, fnType.NumIn())
	for i := 0; i < fnType.NumIn(); i++ {
		dep := fnType.In(i)
		if dep.Kind() != reflect.Interface {
			if dep.Kind() != reflect.Ptr {
				dep = reflect.PtrTo(dep)
			}
		}
		inst, err := c.resolve(dep, "", state)
		if err != nil {
			var circularErr *circularDependencyError
			if errors.As(err, &circularErr) && c.circularDependenciesMode() != "error" {
				args[i] = reflect.Zero(fnType.In(i))
				continue
			}
			return nil, err
		}
		if inst == nil {
			args[i] = reflect.Zero(fnType.In(i))
			continue
		}
		args[i] = reflect.ValueOf(inst)
	}
	return args, nil
}

func (c *Container) fill(target interface{}, state *resolutionState) error {
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return nil
	}

	s := v.Elem()
	rt := s.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if field.Anonymous {
			fType := field.Type
			if fType.Kind() == reflect.Ptr {
				if err := c.fill(s.Field(i).Interface(), state); err != nil {
					return err
				}
			} else if fType.Kind() == reflect.Struct {
				ptr := reflect.NewAt(fType, unsafe.Pointer(s.Field(i).UnsafeAddr()))
				if err := c.fill(ptr.Interface(), state); err != nil {
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

		inst, err := c.resolve(keyType, injectName, state)
		if err != nil {
			var circularErr *circularDependencyError
			if errors.As(err, &circularErr) && c.circularDependenciesMode() != "error" {
				continue
			}
			return err
		}
		if inst == nil {
			continue
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

func newResolutionState() *resolutionState {
	return &resolutionState{
		active: make(map[dependencyKey]struct{}),
	}
}

func (c *Container) enterDependency(key dependencyKey, state *resolutionState) {
	if state == nil {
		return
	}
	state.active[key] = struct{}{}
	state.stack = append(state.stack, key)
}

func (c *Container) leaveDependency(key dependencyKey, state *resolutionState) {
	if state == nil || len(state.stack) == 0 {
		return
	}
	delete(state.active, key)
	state.stack = state.stack[:len(state.stack)-1]
}

func (c *Container) circularDependenciesMode() string {
	switch strings.ToLower(viper.GetString("app.ioc.circularDependenciesMode")) {
	case "skip", "ignore":
		return "skip"
	case "warn", "warning":
		return "warning"
	default:
		return "error"
	}
}

func (c *Container) handleCircularDependency(path string) error {
	err := &circularDependencyError{
		path: fmt.Sprintf("container: circular dependency detected: %s", path),
	}
	switch c.circularDependenciesMode() {
	case "warning":
		log.Log().Warn(err.Error())
		return err
	default:
		return err
	}
}

func (c *Container) resolveCircularDependency(key dependencyKey, state *resolutionState) (interface{}, error) {
	path := c.circularDependencyPath(state.stack, key)
	mode := c.circularDependenciesMode()
	if mode == "error" {
		return nil, c.handleCircularDependency(path)
	}

	if mode == "warning" {
		log.Log().Warn(fmt.Sprintf("container: circular dependency detected: %s", path))
	}

	b := c.findBinding(key.t, key.name)
	if b != nil && b.isSingleton && b.concrete != nil {
		return b.concrete, nil
	}

	return nil, nil
}

func (c *Container) findBinding(t reflect.Type, name string) *binding {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if m, ok := c.bindings[t]; ok {
		if b, found := m[name]; found {
			return b
		}
		if b, found := m[""]; found {
			return b
		}
	}

	return nil
}

func (c *Container) circularDependencyPath(stack []dependencyKey, repeated dependencyKey) string {
	start := 0
	for i, key := range stack {
		if key == repeated {
			start = i
			break
		}
	}

	path := make([]string, 0, len(stack)-start+1)
	for i := start; i < len(stack); i++ {
		path = append(path, dependencyLabel(stack[i]))
	}
	path = append(path, dependencyLabel(repeated))
	return strings.Join(path, " -> ")
}

func dependencyLabel(key dependencyKey) string {
	if key.name == "" {
		return key.t.String()
	}
	return fmt.Sprintf("%s[%s]", key.t.String(), key.name)
}

// hasForeignExplicitBinding reports whether t has an explicit binding that holds an
// instance other than target.
//
// Auto-registered bindings — the ones resolve() creates on demand for a
// pointer-to-struct — deliberately do not count. Otherwise merely having resolved a
// type once would change how Make behaves for it afterwards.
func (c *Container) hasForeignExplicitBinding(t reflect.Type, target interface{}) bool {
	c.mu.RLock()
	b, ok := c.bindings[t][""]
	c.mu.RUnlock()

	if !ok || !b.explicit {
		return false
	}

	b.mu.Lock()
	held := b.concrete
	b.mu.Unlock()

	// The bound instance being filled by its own owner is the supported pattern.
	return held != target
}
