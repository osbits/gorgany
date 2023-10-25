package core

type ISubscriber interface {
	Handle()
}

type IEventBus interface {
	Subscribe(event string, subscriber ISubscriber)
	SubscribeAsync(event string, subscriber ISubscriber)
	Publish(event string, args ...map[string]any) error
	Unsubscribe(event string)
	WaitAsync()
}
