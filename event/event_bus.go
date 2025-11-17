package event

import (
	"context"
	"errors"
	"fmt"
	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/err"
	"reflect"
	"sync"
)

type SubscriptionConfig struct {
	subscriber core.ISubscriber
	async      bool
}

func NewEventBus() core.IEventBus {
	return &EventBus{
		mutex:       sync.Mutex{},
		waitGroup:   new(sync.WaitGroup),
		subscribers: make(map[string]SubscriptionConfig),
	}
}

type EventBus struct {
	mutex       sync.Mutex
	waitGroup   *sync.WaitGroup
	subscribers map[string]SubscriptionConfig
}

func (thiz *EventBus) Subscribe(event string, subscriber core.ISubscriber) error {
	thiz.mutex.Lock()

	rtSubscriber := reflect.TypeOf(subscriber)
	if rtSubscriber.Kind() != reflect.Ptr {
		return errors.New("event_bus: Subscriber must be a pointer")
	}

	thiz.subscribers[event] = SubscriptionConfig{
		subscriber: subscriber,
		async:      false,
	}
	thiz.mutex.Unlock()

	return nil
}

func (thiz *EventBus) SubscribeAsync(event string, subscriber core.ISubscriber) error {
	thiz.mutex.Lock()

	rtSubscriber := reflect.TypeOf(subscriber)
	if rtSubscriber.Kind() != reflect.Ptr {
		return errors.New("event_bus: Subscriber must be a pointer")
	}

	thiz.subscribers[event] = SubscriptionConfig{
		subscriber: subscriber,
		async:      true,
	}
	thiz.mutex.Unlock()

	return nil
}

func (thiz *EventBus) Publish(ctx context.Context, event string, args ...map[string]any) error {
	subscriptionConfig, ok := thiz.subscribers[event]
	if !ok {
		return fmt.Errorf("event_bus: subscription `%s` not found", event)
	}
	subscriberRaw := subscriptionConfig.subscriber
	rtSubscriber := reflect.TypeOf(subscriberRaw)

	if rtSubscriber.Kind() != reflect.Ptr {
		return errors.New("event_bus: Subscriber must be a pointer")
	}

	subscriber := subscriberRaw.(core.ISubscriber)
	if subscriptionConfig.async {
		thiz.doPublishAsync(ctx, subscriber)
	} else {
		thiz.doPublish(ctx, subscriber)
	}

	return nil
}

func (thiz *EventBus) Unsubscribe(key string) {
	thiz.mutex.Lock()
	delete(thiz.subscribers, key)
	thiz.mutex.Unlock()
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
		defer func() {
			thiz.waitGroup.Done()

			if r := recover(); r != nil {
				err.HandleErrorWithStacktrace(r)
			}
		}()
		subscriber.Handle(ctx)
	}()
}
