package service

import (
	"reflect"
	"testing"

	"github.com/osbits/gorgany/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type frameworkValidator struct {
	core.IValidator
	id string
}

type appService struct {
	id string
}

// TestRebindingACoreInterfaceStillWorks is the constraint the fix had to respect:
// overwriting is currently the only mechanism apps have for overriding a framework
// service, so it must keep working. The warning is the addition, not a refusal.
func TestRebindingACoreInterfaceStillWorks(t *testing.T) {
	c := NewContainer()

	require.NoError(t, c.SingletonLazy(func() core.IValidator {
		return &frameworkValidator{id: "framework"}
	}))
	require.NoError(t, c.SingletonLazy(func() core.IValidator {
		return &frameworkValidator{id: "app-override"}
	}))

	var resolved core.IValidator
	require.NoError(t, c.Make(&resolved))

	assert.Equal(t, "app-override", resolved.(*frameworkValidator).id,
		"the last registration must win — register your override provider last")
}

// TestLastRegistrationWinsForEveryCoreInterface pins the documented rule across a
// few of the extension points apps actually override.
func TestLastRegistrationWinsForEveryCoreInterface(t *testing.T) {
	c := NewContainer()

	require.NoError(t, c.SingletonLazy(func() core.IDataContext { return &stubDataContext{id: "first"} }))
	require.NoError(t, c.SingletonLazy(func() core.IDataContext { return &stubDataContext{id: "second"} }))

	var resolved core.IDataContext
	require.NoError(t, c.Make(&resolved))
	assert.Equal(t, "second", resolved.(*stubDataContext).id)
}

// TestIsCoreAbstractionScopesTheWarning: the warning is deliberately limited to the
// framework's own interfaces. An app rebinding its own service is ordinary; it is
// replacing a framework contract where registration order silently decides
// behaviour.
func TestIsCoreAbstractionScopesTheWarning(t *testing.T) {
	assert.True(t, isCoreAbstraction(reflectTypeOf[core.IValidator]()))
	assert.True(t, isCoreAbstraction(reflectTypeOf[core.IDataContext]()))
	assert.True(t, isCoreAbstraction(reflectTypeOf[core.IDBContext]()))

	// Not a core interface.
	assert.False(t, isCoreAbstraction(reflectTypeOf[error]()))
	// Not an interface at all.
	assert.False(t, isCoreAbstraction(reflectTypeOf[*appService]()))
}

// TestRebindingANonCoreTypeIsSilent
func TestRebindingANonCoreTypeIsSilent(t *testing.T) {
	c := NewContainer()

	require.NoError(t, c.SingletonLazy(func() *appService { return &appService{id: "first"} }))
	require.NoError(t, c.SingletonLazy(func() *appService { return &appService{id: "second"} }))

	var resolved *appService
	require.NoError(t, c.Resolve(&resolved))
	assert.Equal(t, "second", resolved.id)
}

func TestNamedSuffix(t *testing.T) {
	assert.Equal(t, "", namedSuffix(""))
	assert.Equal(t, "[replica]", namedSuffix("replica"))
}

// TestNamedBindingsDoNotCollideWithUnnamedOnes guards that the rebind detection is
// keyed on (type, name) rather than type alone.
func TestNamedBindingsDoNotCollideWithUnnamedOnes(t *testing.T) {
	c := NewContainer()

	require.NoError(t, c.SingletonLazy(func() core.IValidator {
		return &frameworkValidator{id: "unnamed"}
	}))
	require.NoError(t, c.NamedSingletonLazy("strict", func() core.IValidator {
		return &frameworkValidator{id: "named"}
	}))

	var unnamed core.IValidator
	require.NoError(t, c.Resolve(&unnamed))
	assert.Equal(t, "unnamed", unnamed.(*frameworkValidator).id)

	var named core.IValidator
	require.NoError(t, c.NamedResolve(&named, "strict"))
	assert.Equal(t, "named", named.(*frameworkValidator).id)
}

type stubDataContext struct {
	core.IDataContext
	id string
}

// reflectTypeOf returns the reflect.Type for T, including for interface types.
func reflectTypeOf[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}

// ---------------------------------------------------------------- B4: Make intent

// schedulerLike stands in for *gocron.Scheduler: a third-party struct with no
// container:"inject" tags, which a caller might mistakenly try to obtain with Make.
type schedulerLike struct {
	jobs    int
	started bool
}

// TestMakeRefusesAForeignInstanceOfABoundType is the B4 regression, in the exact
// shape that broke the job subsystem. JobProvider.Boot did:
//
//	sched := &gocron.Scheduler{}
//	c.Make(sched)     // field injection on a type with no inject tags
//	go sched.Start()  // ticks the zero value
//
// Make returned nil and left sched as the zero value, so an empty scheduler was
// started and no job ever ran.
func TestMakeRefusesAForeignInstanceOfABoundType(t *testing.T) {
	c := NewContainer()

	registered := &schedulerLike{jobs: 7, started: true}
	require.NoError(t, c.SingletonLazy(func() *schedulerLike { return registered }))

	// The A2 mistake: a fresh zero value, expecting to receive the registered one.
	fresh := &schedulerLike{}
	err := c.Make(fresh)

	require.Error(t, err, "Make must not silently field-inject when resolution was wanted")
	assert.Contains(t, err.Error(), "Resolve", "the error must name the method the caller wanted")
	assert.Contains(t, err.Error(), "schedulerLike")
	assert.Zero(t, fresh.jobs, "and it must not have pretended to succeed")
}

// TestResolveIsTheWayToObtainABoundStruct shows the correct call the error points at.
func TestResolveIsTheWayToObtainABoundStruct(t *testing.T) {
	c := NewContainer()

	registered := &schedulerLike{jobs: 7, started: true}
	require.NoError(t, c.SingletonLazy(func() *schedulerLike { return registered }))

	var resolved *schedulerLike
	require.NoError(t, c.Resolve(&resolved))
	assert.Same(t, registered, resolved)
	assert.Equal(t, 7, resolved.jobs)
}

// TestMakeStillFillsAnUnboundStruct is the overwhelmingly common use, and must be
// untouched: controllers, middlewares, commands and jobs are all filled this way.
func TestMakeStillFillsAnUnboundStruct(t *testing.T) {
	c := NewContainer()
	require.NoError(t, c.SingletonLazy(func() core.IValidator {
		return &frameworkValidator{id: "v"}
	}))

	target := &struct {
		Validator core.IValidator `container:"inject"`
	}{}

	require.NoError(t, c.Make(target))
	require.NotNil(t, target.Validator, "an unbound struct must still be injected")
}

// TestMakeOnTheBoundInstanceItselfIsAllowed keeps the supported pattern working:
// bind a concrete pointer, then fill that very instance's fields.
func TestMakeOnTheBoundInstanceItselfIsAllowed(t *testing.T) {
	c := NewContainer()

	type service struct {
		Validator core.IValidator `container:"inject"`
	}

	// The validator first: Singleton is non-lazy, so it resolves at bind time and
	// its own field injection needs the dependency already present.
	require.NoError(t, c.SingletonLazy(func() core.IValidator {
		return &frameworkValidator{id: "v"}
	}))

	instance := &service{}
	require.NoError(t, c.Singleton(func() *service { return instance }))

	require.NoError(t, c.Make(instance),
		"filling the bound instance's own fields is the supported pattern")
	assert.NotNil(t, instance.Validator)
}

// TestAutoRegisteredBindingsDoNotChangeMakeBehaviour: resolve() auto-registers a
// binding for any pointer-to-struct it is asked for. Merely having resolved a type
// once must not make a later Make on a fresh one fail.
func TestAutoRegisteredBindingsDoNotChangeMakeBehaviour(t *testing.T) {
	c := NewContainer()
	require.NoError(t, c.SingletonLazy(func() core.IValidator {
		return &frameworkValidator{id: "v"}
	}))

	type injected struct {
		Validator core.IValidator `container:"inject"`
	}

	// Resolving auto-registers *injected.
	var first *injected
	require.NoError(t, c.Resolve(&first))
	require.NotNil(t, first)

	// A later Make on a different instance must still just fill it.
	second := &injected{}
	require.NoError(t, c.Make(second),
		"an auto-registered binding must not turn Make into an error")
	assert.NotNil(t, second.Validator)
}

// TestMakeOnAnInterfacePointerStillResolves pins the other branch, which every
// provider relies on.
func TestMakeOnAnInterfacePointerStillResolves(t *testing.T) {
	c := NewContainer()
	require.NoError(t, c.SingletonLazy(func() core.IValidator {
		return &frameworkValidator{id: "v"}
	}))

	var resolved core.IValidator
	require.NoError(t, c.Make(&resolved))
	require.NotNil(t, resolved)
	assert.Equal(t, "v", resolved.(*frameworkValidator).id)
}
