package provider

import "github.com/osbits/gorgany/app/core"

// Compile-time proof that every provider this package ships actually satisfies
// core.IProvider.
//
// EventProvider shipped for several releases with a `Boot(core.IContainer) error`
// signature, which does not satisfy the interface it exists to implement — so
// NewEventProvider() could never be passed to Bootstrapper.AddProvider and the
// whole events subsystem was unreachable. Nothing caught it because no call site
// in the repo used it. These assertions make that class of mistake a build error.
//
// Add a line here whenever a provider is added to this package.
var (
	_ core.IProvider = (*CommandProvider)(nil)
	_ core.IProvider = (*DbProvider)(nil)
	_ core.IProvider = (*ErrorProvider)(nil)
	_ core.IProvider = (*EventProvider)(nil)
	_ core.IProvider = (*I18nProvider)(nil)
	_ core.IProvider = (*JobProvider)(nil)
	_ core.IProvider = (*LoggerProvider)(nil)
	_ core.IProvider = (*RouteProvider)(nil)
	_ core.IProvider = (*ViewProvider)(nil)

	// AppProvider declares its methods on the value type, not a pointer.
	_ core.IProvider = AppProvider{}

	_ core.Bootstrapper = (*Bootstrapper)(nil)
)
