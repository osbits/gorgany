package core

import "context"

// ISubscriber defines the interface for event subscribers
type ISubscriber interface {
	// Handle processes the event with the given context
	Handle(ctx context.Context)
}

// IEventBus defines the interface for event bus management
type IEventBus interface {
	// Subscribe registers a synchronous subscriber for an event
	Subscribe(event string, subscriber ISubscriber) error
	// SubscribeAsync registers an asynchronous subscriber for an event
	SubscribeAsync(event string, subscriber ISubscriber) error
	// Publish publishes an event with optional arguments
	Publish(ctx context.Context, event string, args ...map[string]any) error
	// Unsubscribe removes all subscribers for an event
	Unsubscribe(event string)
	// WaitAsync waits for all asynchronous subscribers to complete
	WaitAsync()
}
