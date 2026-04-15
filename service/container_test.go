package service

import (
	"bytes"
	"errors"
	stdlog "log"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

type Shape interface {
	SetArea(int)
	GetArea() int
}

type Circle struct {
	a int
}

func (c *Circle) SetArea(a int) {
	c.a = a
}

func (c Circle) GetArea() int {
	return c.a
}

type Database interface {
	Connect() bool
}

type MySQL struct{}

func (m MySQL) Connect() bool {
	return true
}

type FigureService struct{}

type Figure struct {
	service *FigureService `container:"inject"`
}

type RectangleService struct{}

type Rectangle struct {
	Figure
	rectService *RectangleService `container:"inject"`
}

type InitService struct {
	initialized bool
}

func (i *InitService) Init() {
	i.initialized = true
}

type CountedDependency struct {
	InitCalls int
}

func (d *CountedDependency) Init() {
	d.InitCalls++
}

type CountedService struct {
	Dependency *CountedDependency `container:"inject"`
}

type CircularA struct {
	B *CircularB `container:"inject"`
}

type CircularB struct {
	A *CircularA `container:"inject"`
}

var instance = NewContainer()

func TestContainer_Singleton(t *testing.T) {
	err := instance.Singleton(func() Shape {
		return &Circle{a: 13}
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(s1 Shape) {
		s1.SetArea(666)
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(s2 Shape) {
		a := s2.GetArea()
		assert.Equal(t, a, 666)
	})
	assert.NoError(t, err)
}

func TestContainer_SingletonLazy(t *testing.T) {
	err := instance.SingletonLazy(func() Shape {
		return &Circle{a: 13}
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(s1 Shape) {
		s1.SetArea(666)
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(s2 Shape) {
		a := s2.GetArea()
		assert.Equal(t, a, 666)
	})
	assert.NoError(t, err)
}

func TestContainer_Singleton_With_Missing_Dependency_Resolve(t *testing.T) {
	err := instance.Singleton(func(db Database) Shape {
		return &Circle{a: 13}
	})
	assert.EqualError(t, err, "container: no binding for type service.Database")
}

func TestContainer_Singleton_With_Resolve_That_Returns_Nothing(t *testing.T) {
	err := instance.Singleton(func() {})
	assert.EqualError(t, err, "container: resolver must return (instance [, error])")
}

func TestContainer_SingletonLazy_With_Resolve_That_Returns_Nothing(t *testing.T) {
	err := instance.SingletonLazy(func() {})
	assert.EqualError(t, err, "container: resolver must return (instance [, error])")
}

func TestContainer_Singleton_With_Resolve_That_Returns_Error(t *testing.T) {
	err := instance.Singleton(func() (Shape, error) {
		return nil, errors.New("app: error")
	})
	assert.Error(t, err, "app: error")
}

func TestContainer_SingletonLazy_With_Resolve_That_Returns_Error(t *testing.T) {
	err := instance.SingletonLazy(func() (Shape, error) {
		return nil, errors.New("app: error")
	})
	assert.NoError(t, err)

	var s Shape
	err = instance.Resolve(&s)
	assert.Error(t, err, "app: error")
}

func TestContainer_Singleton_With_NonFunction_Resolver_It_Should_Fail(t *testing.T) {
	err := instance.Singleton("STRING!")
	assert.EqualError(t, err, "container: resolver must be a function")
}

func TestContainer_SingletonLazy_With_NonFunction_Resolver_It_Should_Fail(t *testing.T) {
	err := instance.SingletonLazy("STRING!")
	assert.EqualError(t, err, "container: resolver must be a function")
}

func TestContainer_Singleton_With_Resolvable_Arguments(t *testing.T) {
	err := instance.Singleton(func() Shape {
		return &Circle{a: 666}
	})
	assert.NoError(t, err)

	err = instance.Singleton(func(s Shape) Database {
		assert.Equal(t, s.GetArea(), 666)
		return &MySQL{}
	})
	assert.NoError(t, err)
}

func TestContainer_SingletonLazy_With_Resolvable_Arguments(t *testing.T) {
	err := instance.SingletonLazy(func() Shape {
		return &Circle{a: 666}
	})
	assert.NoError(t, err)

	err = instance.SingletonLazy(func(s Shape) Database {
		assert.Equal(t, s.GetArea(), 666)
		return &MySQL{}
	})
	assert.NoError(t, err)

	var s Shape
	err = instance.Resolve(&s)
	assert.NoError(t, err)
}

func TestContainer_Singleton_With_Non_Resolvable_Arguments(t *testing.T) {
	instance.Reset()

	err := instance.Singleton(func(s Shape) Shape {
		return &Circle{a: s.GetArea()}
	})
	assert.EqualError(t, err, "container: resolver cannot depend on its own return type service.Shape")
}

func TestContainer_SingletonLazy_With_Non_Resolvable_Arguments(t *testing.T) {
	instance.Reset()

	err := instance.SingletonLazy(func(s Shape) Shape {
		return &Circle{a: s.GetArea()}
	})
	assert.EqualError(t, err, "container: resolver cannot depend on its own return type service.Shape")
}

func TestContainer_NamedSingleton(t *testing.T) {
	err := instance.NamedSingleton("theCircle", func() Shape {
		return &Circle{a: 13}
	})
	assert.NoError(t, err)

	var sh Shape
	err = instance.NamedResolve(&sh, "theCircle")
	assert.NoError(t, err)
	assert.Equal(t, sh.GetArea(), 13)
}

func TestContainer_NamedSingletonLazy(t *testing.T) {
	err := instance.NamedSingletonLazy("theCircle", func() Shape {
		return &Circle{a: 13}
	})
	assert.NoError(t, err)

	var sh Shape
	err = instance.NamedResolve(&sh, "theCircle")
	assert.NoError(t, err)
	assert.Equal(t, sh.GetArea(), 13)
}

func TestContainer_Transient(t *testing.T) {
	err := instance.Transient(func() Shape {
		return &Circle{a: 666}
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(s1 Shape) {
		s1.SetArea(13)
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(s2 Shape) {
		a := s2.GetArea()
		assert.Equal(t, a, 666)
	})
	assert.NoError(t, err)
}

func TestContainer_TransientLazy(t *testing.T) {
	err := instance.TransientLazy(func() Shape {
		return &Circle{a: 666}
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(s1 Shape) {
		s1.SetArea(13)
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(s2 Shape) {
		a := s2.GetArea()
		assert.Equal(t, a, 666)
	})
	assert.NoError(t, err)
}

func TestContainer_Transient_With_Resolve_That_Returns_Nothing(t *testing.T) {
	err := instance.Transient(func() {})
	assert.EqualError(t, err, "container: resolver must return (instance [, error])")
}

func TestContainer_Transient_With_Resolve_That_Returns_Error(t *testing.T) {
	err := instance.Transient(func() (Shape, error) {
		return nil, errors.New("app: error")
	})
	assert.Error(t, err, "app: error")

	firstInvoke := true
	err = instance.Transient(func() (Database, error) {
		if firstInvoke {
			firstInvoke = false
			return &MySQL{}, nil
		}
		return nil, errors.New("app: second call error")
	})
	assert.NoError(t, err)

	var db Database
	err = instance.Resolve(&db)
	assert.Error(t, err, "app: second call error")
}

func TestContainer_Transient_With_Resolve_With_Invalid_Signature_It_Should_Fail(t *testing.T) {
	err := instance.Transient(func() (Shape, Database, error) {
		return nil, nil, nil
	})
	assert.EqualError(t, err, "container: resolver must return (instance [, error])")
}

func TestContainer_NamedTransient(t *testing.T) {
	err := instance.NamedTransient("theCircle", func() Shape {
		return &Circle{a: 13}
	})
	assert.NoError(t, err)

	var sh Shape
	err = instance.NamedResolve(&sh, "theCircle")
	assert.NoError(t, err)
	assert.Equal(t, sh.GetArea(), 13)
}

func TestContainer_NamedTransientLazy(t *testing.T) {
	err := instance.NamedTransientLazy("theCircle", func() Shape {
		return &Circle{a: 13}
	})
	assert.NoError(t, err)

	var sh Shape
	err = instance.NamedResolve(&sh, "theCircle")
	assert.NoError(t, err)
	assert.Equal(t, sh.GetArea(), 13)
}

func TestContainer_Invoke_With_Multiple_Resolving(t *testing.T) {
	err := instance.Singleton(func() Shape {
		return &Circle{a: 5}
	})
	assert.NoError(t, err)

	err = instance.Singleton(func() Database {
		return &MySQL{}
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(s Shape, m Database) {
		if _, ok := s.(*Circle); !ok {
			t.Error("Expected Circle")
		}

		if _, ok := m.(*MySQL); !ok {
			t.Error("Expected MySQL")
		}
	})
	assert.NoError(t, err)
}

func TestContainer_Invoke_With_Dependency_Missing_In_Chain(t *testing.T) {
	var instance = NewContainer()
	err := instance.SingletonLazy(func() (Database, error) {
		var s Shape
		if err := instance.Resolve(&s); err != nil {
			return nil, err
		}
		return &MySQL{}, nil
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(m Database) {
		if _, ok := m.(*MySQL); !ok {
			t.Error("Expected MySQL")
		}
	})
	assert.EqualError(t, err, "container: no binding for type service.Shape")
}

func TestContainer_Invoke_With_Unsupported_Receiver_It_Should_Fail(t *testing.T) {
	err := instance.Invoke("STRING!")
	assert.EqualError(t, err, "container: invalid function")
}

func TestContainer_Invoke_With_Second_UnBounded_Argument(t *testing.T) {
	instance.Reset()

	err := instance.Singleton(func() Shape {
		return &Circle{}
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(s Shape, d Database) {})
	assert.EqualError(t, err, "container: no binding for type service.Database")
}

func TestContainer_Invoke_With_A_Returning_Error(t *testing.T) {
	instance.Reset()

	err := instance.Singleton(func() Shape {
		return &Circle{}
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(s Shape) error {
		return errors.New("app: some context error")
	})
	assert.EqualError(t, err, "app: some context error")
}

func TestContainer_Invoke_With_A_Returning_Nil_Error(t *testing.T) {
	instance.Reset()

	err := instance.Singleton(func() Shape {
		return &Circle{}
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(s Shape) error {
		return nil
	})
	assert.Nil(t, err)
}

func TestContainer_Invoke_With_Invalid_Signature(t *testing.T) {
	instance.Reset()

	err := instance.Singleton(func() Shape {
		return &Circle{}
	})
	assert.NoError(t, err)

	err = instance.Invoke(func(s Shape) (int, error) {
		return 13, errors.New("app: some context error")
	})
	assert.EqualError(t, err, "app: some context error")
}

func TestContainer_Resolve_With_Reference_As_Resolver(t *testing.T) {
	err := instance.Singleton(func() Shape {
		return &Circle{a: 5}
	})
	assert.NoError(t, err)

	err = instance.Singleton(func() Database {
		return &MySQL{}
	})
	assert.NoError(t, err)

	var (
		s Shape
		d Database
	)

	err = instance.Resolve(&s)
	assert.NoError(t, err)
	if _, ok := s.(*Circle); !ok {
		t.Error("Expected Circle")
	}

	err = instance.Resolve(&d)
	assert.NoError(t, err)
	if _, ok := d.(*MySQL); !ok {
		t.Error("Expected MySQL")
	}
}

func TestContainer_Resolve_With_Unsupported_Receiver_It_Should_Fail(t *testing.T) {
	err := instance.Resolve("STRING!")
	assert.EqualError(t, err, "container: abstraction must be pointer to interface")
}

func TestContainer_Resolve_With_NonReference_Receiver_It_Should_Fail(t *testing.T) {
	var s Shape
	err := instance.Resolve(s)
	assert.EqualError(t, err, "container: abstraction must be pointer to interface")
}

func TestContainer_Resolve_With_UnBounded_Reference_It_Should_Fail(t *testing.T) {
	instance.Reset()

	var s Shape
	err := instance.Resolve(&s)
	assert.EqualError(t, err, "container: no binding for type service.Shape")
}

func TestContainer_Fill_With_Struct_Pointer(t *testing.T) {
	err := instance.Singleton(func() Shape {
		return &Circle{a: 5}
	})
	assert.NoError(t, err)

	err = instance.NamedSingleton("C", func() Shape {
		return &Circle{a: 5}
	})
	assert.NoError(t, err)

	err = instance.Singleton(func() Database {
		return &MySQL{}
	})
	assert.NoError(t, err)

	myApp := struct {
		S Shape    `container:"inject"`
		D Database `container:"inject"`
		C Shape    `container:"inject"`
		X string
	}{}

	err = instance.Make(&myApp)
	assert.NoError(t, err)

	assert.IsType(t, &Circle{}, myApp.S)
	assert.IsType(t, &MySQL{}, myApp.D)
}

func TestContainer_Fill_Unexported_With_Struct_Pointer(t *testing.T) {
	err := instance.Singleton(func() Shape {
		return &Circle{a: 5}
	})
	assert.NoError(t, err)

	err = instance.Singleton(func() Database {
		return &MySQL{}
	})
	assert.NoError(t, err)

	myApp := struct {
		s Shape    `container:"inject"`
		d Database `container:"inject"`
		y int
	}{}

	err = instance.Make(&myApp)
	assert.NoError(t, err)

	assert.IsType(t, &Circle{}, myApp.s)
	assert.IsType(t, &MySQL{}, myApp.d)
}

func TestContainer_Fill_With_Invalid_Struct_It_Should_Fail(t *testing.T) {
	invalidStruct := 0
	err := instance.Make(&invalidStruct)
	assert.EqualError(t, err, "container: no binding for type int")
}

func TestContainer_Fill_With_Invalid_Pointer_It_Should_Fail(t *testing.T) {
	var s Shape
	err := instance.Make(s)
	assert.EqualError(t, err, "container: Make requires a pointer")
}

func TestContainer_Fill_With_Dependency_Missing_In_Chain(t *testing.T) {
	var instance = NewContainer()

	err := instance.NamedSingletonLazy("C", func() (Shape, error) {
		var s Shape
		if err := instance.NamedResolve(&s, "foo"); err != nil {
			return nil, err
		}
		return &Circle{a: 5}, nil
	})
	assert.NoError(t, err)

	err = instance.Singleton(func() Database {
		return &MySQL{}
	})
	assert.NoError(t, err)

	myApp := struct {
		S Shape    `container:"inject"`
		D Database `container:"inject"`
		C Shape    `container:"inject"`
		X string
	}{}

	err = instance.Make(&myApp)
	assert.EqualError(t, err, "container: no binding for type service.Shape")
}

func TestContainer_Fill_With_Embedded_Struct(t *testing.T) {
	var instance = NewContainer()

	rect := &Rectangle{}
	err := instance.Make(rect)
	assert.NoError(t, err)

	assert.NotNil(t, rect.rectService)
	assert.NotNil(t, rect.service)
}

func TestContainer_Init_Interface(t *testing.T) {
	var instance = NewContainer()

	initService := &InitService{}
	err := instance.Singleton(func() *InitService {
		return initService
	})
	assert.NoError(t, err)

	err = instance.Make(initService)
	assert.NoError(t, err)
	assert.True(t, initService.initialized)
}

func TestContainer_Init_Interface_With_Interface_Binding(t *testing.T) {
	var instance = NewContainer()

	initService := &InitService{}
	err := instance.Singleton(func() interface{} {
		return initService
	})
	assert.NoError(t, err)

	err = instance.Make(initService)
	assert.NoError(t, err)
	assert.True(t, initService.initialized)
}

func TestContainer_Init_Interface_With_Named_Binding(t *testing.T) {
	var instance = NewContainer()

	initService := &InitService{}
	err := instance.NamedSingleton("initService", func() *InitService {
		return initService
	})
	assert.NoError(t, err)

	err = instance.Make(initService)
	assert.NoError(t, err)
	assert.True(t, initService.initialized)
}

func TestContainer_Singleton_Dependencies_Are_Injected_Once(t *testing.T) {
	var instance = NewContainer()

	err := instance.SingletonLazy(func() *CountedDependency {
		return &CountedDependency{}
	})
	assert.NoError(t, err)

	err = instance.SingletonLazy(func() *CountedService {
		return &CountedService{}
	})
	assert.NoError(t, err)

	var first *CountedService
	err = instance.Resolve(&first)
	assert.NoError(t, err)
	assert.NotNil(t, first)
	assert.NotNil(t, first.Dependency)
	assert.Equal(t, 1, first.Dependency.InitCalls)

	var second *CountedService
	err = instance.Resolve(&second)
	assert.NoError(t, err)
	assert.Same(t, first, second)
	assert.Same(t, first.Dependency, second.Dependency)
	assert.Equal(t, 1, second.Dependency.InitCalls)
}

func TestContainer_CircularDependenciesMode_Error(t *testing.T) {
	var instance = NewContainer()

	t.Cleanup(func() {
		viper.Set("app.ioc.circularDependenciesMode", "")
	})
	viper.Set("app.ioc.circularDependenciesMode", "error")

	var a *CircularA
	err := instance.Resolve(&a)
	assert.EqualError(t, err, "container: circular dependency detected: *service.CircularA -> *service.CircularB -> *service.CircularA")
}

func TestContainer_CircularDependenciesMode_Warning(t *testing.T) {
	var instance = NewContainer()

	t.Cleanup(func() {
		viper.Set("app.ioc.circularDependenciesMode", "")
	})
	viper.Set("app.ioc.circularDependenciesMode", "warning")

	var logBuffer bytes.Buffer
	prevWriter := stdlog.Writer()
	stdlog.SetOutput(&logBuffer)
	t.Cleanup(func() {
		stdlog.SetOutput(prevWriter)
	})

	var a *CircularA
	err := instance.Resolve(&a)
	assert.NoError(t, err)
	assert.NotNil(t, a)
	assert.NotNil(t, a.B)
	assert.Same(t, a, a.B.A)
	assert.Contains(t, logBuffer.String(), "circular dependency detected")
}

func TestContainer_CircularDependenciesMode_Skip(t *testing.T) {
	var instance = NewContainer()

	t.Cleanup(func() {
		viper.Set("app.ioc.circularDependenciesMode", "")
	})
	viper.Set("app.ioc.circularDependenciesMode", "skip")

	var a *CircularA
	err := instance.Resolve(&a)
	assert.NoError(t, err)
	assert.NotNil(t, a)
	assert.NotNil(t, a.B)
	assert.Same(t, a, a.B.A)
}
