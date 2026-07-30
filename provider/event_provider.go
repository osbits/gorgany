package provider

import (
	"fmt"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/event"
)

type EventProvider struct {
	subs []subscription
}

type subscription struct {
	event string
	ctor  func() core.ISubscriber
	async bool
}

func NewEventProvider() *EventProvider {
	return &EventProvider{subs: make([]subscription, 0)}
}

// RegisterSubscriber добавляет sync подписку с фабрикой подписчика.
func (p *EventProvider) RegisterSubscriber(eventName string, ctor func() core.ISubscriber) {
	p.subs = append(p.subs, subscription{event: eventName, ctor: ctor, async: false})
}

func (p *EventProvider) RegisterAsyncSubscriber(eventName string, ctor func() core.ISubscriber) {
	p.subs = append(p.subs, subscription{event: eventName, ctor: ctor, async: true})
}

func (p *EventProvider) Register(c core.IContainer) {
	c.SingletonLazy(func() core.IEventBus {
		return event.NewEventBus()
	})
	for _, sub := range p.subs {
		c.TransientLazy(func(ctor func() core.ISubscriber) func() core.ISubscriber {
			return ctor
		}(sub.ctor))
	}
}

// Boot wires every registered subscriber onto the event bus.
//
// It satisfies core.IProvider, which has no error return, so an unrecoverable
// wiring failure panics — an app that boots with its subscribers silently
// missing is worse than one that refuses to boot. The fallible work lives in
// boot() so it can be asserted on directly.
func (p *EventProvider) Boot(c core.IContainer) {
	if err := p.boot(c); err != nil {
		panic(err)
	}
}

func (p *EventProvider) boot(c core.IContainer) error {
	var bus core.IEventBus
	if err := c.Make(&bus); err != nil {
		return fmt.Errorf("event Boot: cannot Make EventBus: %w", err)
	}

	for _, sub := range p.subs {
		inst := sub.ctor()
		if err := c.Make(&inst); err != nil {
			return fmt.Errorf("event Boot: cannot make subscriber %T: %w", inst, err)
		}

		if sub.async {
			if err := bus.SubscribeAsync(sub.event, inst); err != nil {
				return fmt.Errorf("event Boot: SubscribeAsync '%s': %w", sub.event, err)
			}
		} else {
			if err := bus.Subscribe(sub.event, inst); err != nil {
				return fmt.Errorf("event Boot: Subscribe '%s': %w", sub.event, err)
			}
		}
	}
	return nil
}
