package provider

import (
	"context"
	"testing"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingSubscriber struct {
	calls *int
}

func (s *recordingSubscriber) Handle(_ context.Context) {
	*s.calls++
}

// TestEventProviderSatisfiesIProvider is the regression test for the defect that
// made the events subsystem unreachable: EventProvider.Boot returned an error,
// so *EventProvider did not implement core.IProvider and could not be handed to
// Bootstrapper.AddProvider at all.
func TestEventProviderSatisfiesIProvider(t *testing.T) {
	var p any = NewEventProvider()

	_, ok := p.(core.IProvider)
	assert.True(t, ok, "*EventProvider must satisfy core.IProvider")
}

// TestEventProviderCanBeAddedToBootstrapper pins the actual usage that used to
// fail to compile, end to end: register a subscriber, boot, publish, observe.
func TestEventProviderCanBeAddedToBootstrapper(t *testing.T) {
	calls := 0

	p := NewEventProvider()
	p.RegisterSubscriber("user.created", func() core.ISubscriber {
		return &recordingSubscriber{calls: &calls}
	})

	b := NewGorganyBootstrapper()
	b.AddProvider(p)

	c := service.NewContainer()
	require.NotPanics(t, func() { b.Bootstrap(c) })

	var bus core.IEventBus
	require.NoError(t, c.Make(&bus))
	require.NotNil(t, bus)

	require.NoError(t, bus.Publish(context.Background(), "user.created"))
	bus.WaitAsync()

	assert.Equal(t, 1, calls, "the subscriber registered through the provider must fire")
}

// TestAllShippedProvidersSatisfyIProvider mirrors provider_assertions.go at
// runtime so the list cannot silently go stale.
func TestAllShippedProvidersSatisfyIProvider(t *testing.T) {
	candidates := map[string]any{
		"CommandProvider": NewCommandProvider(),
		"DbProvider":      NewDbProvider(),
		"ErrorProvider":   NewErrorProvider(),
		"EventProvider":   NewEventProvider(),
		"I18nProvider":    NewI18nProvider(),
		"JobProvider":     NewJobProvider(),
		"LoggerProvider":  NewLoggerProvider(),
		"RouteProvider":   NewRouteProvider(),
		"ViewProvider":    NewViewProvider(),
		"AppProvider":     AppProvider{},
	}

	for name, candidate := range candidates {
		t.Run(name, func(t *testing.T) {
			_, ok := candidate.(core.IProvider)
			assert.True(t, ok, "%s must satisfy core.IProvider", name)
		})
	}
}
