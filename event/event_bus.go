package event

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/err"
)

type SubscriptionConfig struct {
	subscriber core.ISubscriber
	async      bool
}

func NewEventBus() core.IEventBus {
	return &EventBus{
		waitGroup:   new(sync.WaitGroup),
		subscribers: make(map[string]SubscriptionConfig),
	}
}

// EventBus is a registry of subscribers keyed by event name.
//
// Note the shape of the contract, because "bus" invites a different assumption: there is
// **one subscriber per event name**. Subscribing twice to the same name replaces the first
// silently — it is a dispatch table, not fan-out. That is what core.IEventBus's signature
// allows, and it is pinned by a test so the limitation is visible rather than discovered.
type EventBus struct {
	// mutex guards subscribers. It is an RWMutex because Publish only reads, and Publish
	// is the hot path — every published event took the write lock before, when it took a
	// lock at all.
	mutex       sync.RWMutex
	waitGroup   *sync.WaitGroup
	subscribers map[string]SubscriptionConfig
}

func (thiz *EventBus) Subscribe(event string, subscriber core.ISubscriber) error {
	return thiz.subscribe(event, subscriber, false)
}

func (thiz *EventBus) SubscribeAsync(event string, subscriber core.ISubscriber) error {
	return thiz.subscribe(event, subscriber, true)
}

// subscribe validates before taking the lock, then writes.
//
// Both exported forms used to validate *inside* the critical section and `return` from it
// without unlocking:
//
//	thiz.mutex.Lock()
//	if rtSubscriber.Kind() != reflect.Ptr {
//	    return errors.New(...)   // still holding the lock
//	}
//
// One non-pointer subscriber and every later Subscribe, SubscribeAsync and Unsubscribe
// blocked forever. Validating first means the locked section cannot return early at all,
// which is the same reason Container.storeBinding exists.
func (thiz *EventBus) subscribe(event string, subscriber core.ISubscriber, async bool) error {
	if err := validateSubscriber(subscriber); err != nil {
		return err
	}

	thiz.mutex.Lock()
	defer thiz.mutex.Unlock()

	thiz.subscribers[event] = SubscriptionConfig{
		subscriber: subscriber,
		async:      async,
	}

	return nil
}

// validateSubscriber rejects a subscriber the bus cannot dispatch to.
//
// The nil check is not decoration: reflect.TypeOf(nil) returns nil, so the Kind() call
// below panicked on Subscribe(event, nil) — a nil-pointer dereference where an error was
// obviously intended.
func validateSubscriber(subscriber core.ISubscriber) error {
	if subscriber == nil {
		return errors.New("event_bus: Subscriber must not be nil")
	}

	if reflect.TypeOf(subscriber).Kind() != reflect.Ptr {
		return errors.New("event_bus: Subscriber must be a pointer")
	}

	return nil
}

// Publish dispatches an event to its subscriber.
//
// The map read used to happen with no lock held at all, while every write held one — a data
// race the race detector reports at event_bus.go:40 against :67. On a Go map that can
// escalate to `fatal error: concurrent map read and map write`, which is a runtime fatal:
// RecoveryMiddleware cannot catch it, so it takes the process down rather than the request.
//
// The lock is released before the subscriber runs. Holding it across Handle would serialise
// every publish behind the slowest handler, and a handler that subscribes or unsubscribes
// would deadlock — the mistake this file already made once, and the one Container.bind made.
func (thiz *EventBus) Publish(ctx context.Context, event string, args ...map[string]any) error {
	subscriptionConfig, ok := thiz.lookup(event)
	if !ok {
		return fmt.Errorf("event_bus: subscription `%s` not found", event)
	}

	// subscriber is typed core.ISubscriber on the way in and validated at subscribe time,
	// so there is nothing left to assert here. The previous
	// `subscriberRaw.(core.ISubscriber)` was a no-op on a value that already had that type.
	if subscriptionConfig.async {
		thiz.doPublishAsync(ctx, subscriptionConfig.subscriber)
		return nil
	}

	thiz.doPublish(ctx, subscriptionConfig.subscriber)
	return nil
}

// lookup reads one subscription under the read lock.
func (thiz *EventBus) lookup(event string) (SubscriptionConfig, bool) {
	thiz.mutex.RLock()
	defer thiz.mutex.RUnlock()

	config, ok := thiz.subscribers[event]
	return config, ok
}

func (thiz *EventBus) Unsubscribe(key string) {
	thiz.mutex.Lock()
	defer thiz.mutex.Unlock()

	delete(thiz.subscribers, key)
}

func (thiz *EventBus) WaitAsync() {
	thiz.waitGroup.Wait()
}

func (thiz *EventBus) doPublish(ctx context.Context, subscriber core.ISubscriber) {
	subscriber.Handle(ctx)
}

func (thiz *EventBus) doPublishAsync(ctx context.Context, subscriber core.ISubscriber) {
	thiz.waitGroup.Add(1)
	go func() {
		// recover() first, then Done(). The old order called Done() before recovering,
		// which works — recover is valid anywhere in the deferred function — but it meant
		// WaitAsync could return while a panicking handler was still unwinding.
		defer func() {
			if r := recover(); r != nil {
				err.HandleErrorWithStacktrace(r)
			}
			thiz.waitGroup.Done()
		}()

		subscriber.Handle(ctx)
	}()
}
