package core

type ISubscriber interface {
	Handle()
}

type IEventBus interface {
	Subscribe(event string, subscriber ISubscriber) error
	SubscribeAsync(event string, subscriber ISubscriber) error
	Publish(event string, args ...map[string]any) error
	Unsubscribe(event string)
	WaitAsync()
}
