package provider

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/err"
	eventService "git.qix.sx/gorgany/gorgany.git/event"
	"git.qix.sx/gorgany/gorgany.git/internal"
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
