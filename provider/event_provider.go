package provider

import (
	"gorgany/app/core"
	"gorgany/err"
	eventService "gorgany/event"
	"gorgany/internal"
)

type EventProvider struct {
}

func (thiz EventProvider) InitProvider() {
	internal.GetFrameworkRegistrar().RegisterEventBus(eventService.NewEventBus())
}

func (thiz EventProvider) RegisterEvent(event string, subscriber core.ISubscriber) {
	err.HandleErrorWithStacktrace(eventService.GetEventBus().Subscribe(event, subscriber))
}

func (thiz EventProvider) RegisterAsyncEvent(event string, subscriber core.ISubscriber) {
	err.HandleErrorWithStacktrace(eventService.GetEventBus().SubscribeAsync(event, subscriber))
}
