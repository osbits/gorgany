package core

import "context"

type ISubscriber interface {
	Handle(ctx context.Context)
}

type IEventBus interface {
	Subscribe(event string, subscriber ISubscriber) error
	SubscribeAsync(event string, subscriber ISubscriber) error
	Publish(ctx context.Context, event string, args ...map[string]any) error
	Unsubscribe(event string)
	WaitAsync()
}
