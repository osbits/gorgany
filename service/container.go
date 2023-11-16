package service

import (
	"errors"
	"fmt"
	"gorgany/app/core"
	"gorgany/internal"
	"gorgany/util"
	"reflect"
	"unsafe"
)

func GetContainer() core.IContainer {
	return internal.GetFrameworkRegistrar().GetContainer()
}

// binding holds a resolver and a concrete (if already resolved).
// It is the break for the Container wall!
type binding struct {
	resolver    interface{} // resolver is the function that is responsible for making the concrete.
	concrete    interface{} // concrete is the stored instance for singleton bindings.
	isSingleton bool        // isSingleton is true if the binding is a singleton.
}

// make resolves the binding if needed and returns the resolved concrete.
func (b *binding) make(c Container) (interface{}, error) {
	if b.concrete != nil {
		return b.concrete, nil
	}

	retVal, err := c.invoke(b.resolver)
	if b.isSingleton {
		b.concrete = retVal
	}

	return retVal, err
}

// Container holds the bindings and provides methods to interact with them.
// It is the entry point in the package.
type Container map[reflect.Type]map[string]*binding

// NewContainer creates a new concrete of the Container.
func NewContainer() Container {
	return make(Container)
}

// bind maps an abstraction to concrete and instantiates if it is a singleton binding.
func (thiz Container) bind(resolver interface{}, name string, isSingleton bool, isLazy bool) error {
	reflectedResolver := reflect.TypeOf(resolver)
	if reflectedResolver.Kind() != reflect.Func {
		return errors.New("container: the resolver must be a function")
	}

	if reflectedResolver.NumOut() > 0 {
		if _, exist := thiz[reflectedResolver.Out(0)]; !exist {
			thiz[reflectedResolver.Out(0)] = make(map[string]*binding)
		}
	}

	if err := thiz.validateResolverFunction(reflectedResolver); err != nil {
		return err
	}

	var concrete interface{}
	if !isLazy {
		var err error
		concrete, err = thiz.invoke(resolver)
		if err != nil {
			return err
		}
	}

	if isSingleton {
		thiz[reflectedResolver.Out(0)][name] = &binding{resolver: resolver, concrete: concrete, isSingleton: isSingleton}
	} else {
		thiz[reflectedResolver.Out(0)][name] = &binding{resolver: resolver, isSingleton: isSingleton}
	}

	return nil
}

func (thiz Container) validateResolverFunction(funcType reflect.Type) error {
	retCount := funcType.NumOut()

	if retCount == 0 || retCount > 2 {
		return errors.New("container: resolver function signature is invalid - it must return abstract, or abstract and error")
	}

	resolveType := funcType.Out(0)
	for i := 0; i < funcType.NumIn(); i++ {
		if funcType.In(i) == resolveType {
			return fmt.Errorf("container: resolver function signature is invalid - depends on abstract it returns")
		}
	}

	return nil
}

// invoke calls a function and its returned values.
// It only accepts one value and an optional error.
func (thiz Container) invoke(function interface{}) (interface{}, error) {
	arguments, err := thiz.arguments(function)
	if err != nil {
		return nil, err
	}

	values := reflect.ValueOf(function).Call(arguments)
	if len(values) == 2 && values[1].CanInterface() {
		if err, ok := values[1].Interface().(error); ok {
			return values[0].Interface(), err
		}
	}
	return values[0].Interface(), nil
}

// arguments returns the list of resolved arguments for a function.
func (thiz Container) arguments(function interface{}) ([]reflect.Value, error) {
	reflectedFunction := reflect.TypeOf(function)
	argumentsCount := reflectedFunction.NumIn()
	arguments := make([]reflect.Value, argumentsCount)

	for i := 0; i < argumentsCount; i++ {
		abstraction := reflectedFunction.In(i)
		if concrete, exist := thiz[abstraction][""]; exist {
			instance, err := concrete.make(thiz)
			if err != nil {
				return nil, err
			}
			arguments[i] = reflect.ValueOf(instance)
		} else {
			return nil, errors.New("container: no concrete found for: " + abstraction.String())
		}
	}

	return arguments, nil
}

// Reset deletes all the existing bindings and empties the container.
func (thiz Container) Reset() {
	for k := range thiz {
		delete(thiz, k)
	}
}

// Singleton binds an abstraction to concrete in singleton mode.
// It takes a resolver function that returns the concrete, and its return type matches the abstraction (interface).
// The resolver function can have arguments of abstraction that have been declared in the Container already.
func (thiz Container) Singleton(resolver interface{}) error {
	return thiz.bind(resolver, "", true, false)
}

// SingletonLazy binds an abstraction to concrete lazily in singleton mode.
// The concrete is resolved only when the abstraction is resolved for the first time.
// It takes a resolver function that returns the concrete, and its return type matches the abstraction (interface).
// The resolver function can have arguments of abstraction that have been declared in the Container already.
func (thiz Container) SingletonLazy(resolver interface{}) error {
	return thiz.bind(resolver, "", true, true)
}

// NamedSingleton binds a named abstraction to concrete in singleton mode.
func (thiz Container) NamedSingleton(name string, resolver interface{}) error {
	return thiz.bind(resolver, name, true, false)
}

// NamedSingleton binds a named abstraction to concrete lazily in singleton mode.
// The concrete is resolved only when the abstraction is resolved for the first time.
func (thiz Container) NamedSingletonLazy(name string, resolver interface{}) error {
	return thiz.bind(resolver, name, true, true)
}

// Bind binds an abstraction to concrete in transient mode.
// It takes a resolver function that returns the concrete, and its return type matches the abstraction (interface).
// The resolver function can have arguments of abstraction that have been declared in the Container already.
func (thiz Container) Bind(resolver interface{}) error {
	return thiz.bind(resolver, "", false, false)
}

// BindLazy binds an abstraction to concrete lazily in transient mode.
// Normally the resolver will be called during registration, but that is skipped in lazy mode.
// It takes a resolver function that returns the concrete, and its return type matches the abstraction (interface).
// The resolver function can have arguments of abstraction that have been declared in the Container already.
func (thiz Container) BindLazy(resolver interface{}) error {
	return thiz.bind(resolver, "", false, true)
}

// NamedBind binds a named abstraction to concrete lazily in transient mode.
func (thiz Container) NamedBind(name string, resolver interface{}) error {
	return thiz.bind(resolver, name, false, false)
}

// NamedBindLazy binds a named abstraction to concrete in transient mode.
// Normally the resolver will be called during registration, but that is skipped in lazy mode.
func (thiz Container) NamedBindLazy(name string, resolver interface{}) error {
	return thiz.bind(resolver, name, false, true)
}

// Call takes a receiver function with one or more arguments of the abstractions (interfaces).
// It invokes the receiver function and passes the related concretes.
func (thiz Container) Call(function interface{}) error {
	receiverType := reflect.TypeOf(function)
	if receiverType == nil || receiverType.Kind() != reflect.Func {
		return errors.New("container: invalid function")
	}

	arguments, err := thiz.arguments(function)
	if err != nil {
		return err
	}

	result := reflect.ValueOf(function).Call(arguments)

	if len(result) == 0 {
		return nil
	} else if len(result) == 1 && result[0].CanInterface() {
		if result[0].IsNil() {
			return nil
		}
		if err, ok := result[0].Interface().(error); ok {
			return err
		}
	}

	return errors.New("container: receiver function signature is invalid")
}

// Resolve takes an abstraction (reference of an interface type) and fills it with the related concrete.
func (thiz Container) Resolve(abstraction interface{}) error {
	return thiz.NamedResolve(abstraction, "")
}

// NamedResolve takes abstraction and its name and fills it with the related concrete.
func (thiz Container) NamedResolve(abstraction interface{}, name string) error {
	receiverType := reflect.TypeOf(abstraction)
	if receiverType == nil {
		return errors.New("container: invalid abstraction")
	}

	if receiverType.Kind() == reflect.Ptr {
		elem := receiverType.Elem()

		if concrete, exist := thiz[elem][name]; exist {
			if instance, err := concrete.make(thiz); err == nil {
				reflect.ValueOf(abstraction).Elem().Set(reflect.ValueOf(instance))
				return nil
			} else {
				return fmt.Errorf("container: encountered error while making concrete for: %s. Error encountered: %w", elem.String(), err)
			}
		}

		return errors.New("container: no concrete found for: " + elem.String())
	}

	return errors.New("container: invalid abstraction")
}

func (thiz Container) fill(structure interface{}, chainOfDependencies map[string]interface{}) error {
	receiverType := reflect.TypeOf(structure)
	if receiverType == nil {
		return errors.New("container: invalid structure")
	}

	if receiverType.Kind() == reflect.Ptr {
		elem := receiverType.Elem()
		if elem.Kind() == reflect.Struct {
			s := reflect.ValueOf(structure).Elem()
			rtStruct := s.Type()

			for i := 0; i < s.NumField(); i++ {
				f := s.Field(i)

				if t, exist := s.Type().Field(i).Tag.Lookup("container"); exist {
					name := s.Type().Field(i).Name

					if t != "inject" {
						return fmt.Errorf("container: %v has an invalid struct tag", rtStruct.Field(i).Name)
					}

					if d, ok := chainOfDependencies[f.Type().String()]; ok {
						//log.Log("").Warnf("container: circular dependency detected(struct: %s.%s, field: %s(%s)), "+
						//	"avoid such dependencies, they have an extremely negative impact on the speed of the application.", rtStruct.PkgPath(), rtStruct.Name(), rtStruct.Field(i).Name, f.Type().String())
						ptr := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
						ptr.Set(reflect.ValueOf(d))
						continue
					}

					var concrete *binding
					var ok bool
					if concrete, ok = thiz[f.Type()][name]; !ok {
						concrete = thiz[f.Type()][""]
					}

					var instance any
					if concrete != nil {
						var err error
						instance, err = concrete.make(thiz)
						if err != nil {
							return err
						}

					} else {
						fieldType := util.IndirectType(f.Type())
						fieldValue := reflect.New(fieldType)

						instance = fieldValue.Interface()
					}

					chainOfDependencies[receiverType.String()] = structure
					err := thiz.fill(instance, chainOfDependencies)
					if err != nil {
						return fmt.Errorf("container: cannot make %v field", s.Type().Field(i).Name)
					}

					ptr := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
					ptr.Set(reflect.ValueOf(instance))
				}
			}

			if initiator, ok := structure.(core.Initiator); ok {
				initiator.Init()
			}

			return nil
		}
	}

	return errors.New("container: invalid structure")
}

// Fill takes a struct and resolves the fields with the tag `container:"inject"`
func (thiz Container) Make(structure interface{}, values ...map[string]interface{}) error {
	receiverType := reflect.TypeOf(structure)
	if receiverType == nil {
		return errors.New("container: invalid structure")
	}

	if receiverType.Kind() == reflect.Ptr {
		elem := receiverType.Elem()

		if elem.Kind() == reflect.Struct {
			s := reflect.ValueOf(structure).Elem()
			if len(values) > 0 {
				for fieldName, value := range values[0] {
					f := s.FieldByName(fieldName)
					ptr := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
					ptr.Set(reflect.ValueOf(value))
				}
			}
		}

		return thiz.fill(structure, map[string]any{receiverType.String(): structure})
	}

	return errors.New("container: invalid structure")
}
