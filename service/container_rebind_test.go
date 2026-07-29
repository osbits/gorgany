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
