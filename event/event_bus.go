package event

import (
	"fmt"
	"gorgany/app/core"
	"gorgany/internal"
	"reflect"
	"sync"
	"unsafe"
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

func (thiz *EventBus) Subscribe(event string, subscriber core.ISubscriber) {
	thiz.mutex.Lock()
	thiz.subscribers[event] = SubscriptionConfig{
		subscriber: subscriber,
		async:      false,
	}
	thiz.mutex.Unlock()
}

func (thiz *EventBus) SubscribeAsync(event string, subscriber core.ISubscriber) {
	thiz.mutex.Lock()
	thiz.subscribers[event] = SubscriptionConfig{
		subscriber: subscriber,
		async:      true,
	}
	thiz.mutex.Unlock()
}

func (thiz *EventBus) Publish(event string, args ...map[string]any) error {
	subscriptionConfig, ok := thiz.subscribers[event]
	if !ok {
		return fmt.Errorf("event_bus: subscription `%s` not found", event)
	}
	subscriberRaw := subscriptionConfig.subscriber
	rtSubscriber := reflect.TypeOf(subscriberRaw)
	rvSubscriber := reflect.New(rtSubscriber)
	if len(args) > 0 {
		for key, value := range args[0] {
			f := rvSubscriber.Elem().FieldByName(key)
			ptr := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
			ptr.Set(reflect.ValueOf(value))
		}
	}
	subscriber := rvSubscriber.Interface().(core.ISubscriber)
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
