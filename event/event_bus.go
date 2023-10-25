package event

import (
	"errors"
	"fmt"
	"gorgany/app/core"
	"gorgany/internal"
	"gorgany/service"
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

func GetEventBus() core.IEventBus {
	return internal.GetFrameworkRegistrar().GetEventBus()
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

func (thiz *EventBus) Publish(event string, args ...map[string]any) error {
	subscriptionConfig, ok := thiz.subscribers[event]
	if !ok {
		return fmt.Errorf("event_bus: subscription `%s` not found", event)
	}
	subscriberRaw := subscriptionConfig.subscriber
	rtSubscriber := reflect.TypeOf(subscriberRaw)

	var err error
	if rtSubscriber.Kind() != reflect.Ptr {
		return errors.New("event_bus: Subscriber must be a pointer")
	}
	err = service.GetContainer().Make(subscriberRaw, args...)
	if err != nil {
		return err
	}

	subscriber := subscriberRaw.(core.ISubscriber)
	if subscriptionConfig.async {
		thiz.doPublishAsync(subscriber)
	} else {
		thiz.doPublish(subscriber)
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

func (thiz *EventBus) doPublish(subscriber core.ISubscriber) {
	subscriber.Handle()
}

func (thiz *EventBus) doPublishAsync(subscriber core.ISubscriber) {
	thiz.waitGroup.Add(1)
	go func() {
		defer thiz.waitGroup.Done()
		subscriber.Handle()
	}()
}
