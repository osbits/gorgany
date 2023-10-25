package provider

import (
	"gorgany/event"
	"gorgany/internal"
)

type EventProvider struct {
}

func (thiz EventProvider) InitProvider() {
	internal.GetFrameworkRegistrar().RegisterEventBus(event.NewEventBus())
}
