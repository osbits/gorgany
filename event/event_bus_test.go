package event

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This package had no tests at all, which is how three defects survived in 114 lines: a
// data race on the subscriber map, a lock held across an error return in both Subscribe
// forms, and a nil-pointer dereference on a nil subscriber. Two of the three are the same
// mistakes made elsewhere in the framework and fixed there first.

// ------------------------------------------------------------------ doubles

// countingSubscriber records how many times it was handled.
type countingSubscriber struct {
	calls atomic.Int64
	// block, when set, holds Handle until it is closed — for asserting that the bus does
	// not serialise publishes behind a slow handler.
	block chan struct{}
}

func (s *countingSubscriber) Handle(context.Context) {
	if s.block != nil {
		<-s.block
	}
	s.calls.Add(1)
}

// panickingSubscriber panics, to check the async path recovers.
type panickingSubscriber struct{ handled atomic.Bool }

func (s *panickingSubscriber) Handle(context.Context) {
	s.handled.Store(true)
	panic("subscriber blew up")
}

// reentrantSubscriber subscribes from inside Handle, which deadlocks if Publish holds the
// lock across the call.
type reentrantSubscriber struct {
	bus core.IEventBus
	err error
}

func (s *reentrantSubscriber) Handle(context.Context) {
	s.err = s.bus.Subscribe("added.from.inside", &countingSubscriber{})
}

// valueSubscriber is a non-pointer implementation, which the bus refuses.
type valueSubscriber struct{}

func (valueSubscriber) Handle(context.Context) {}

// completesWithin reports whether fn returned before the deadline. A deadlock has to fail
// with a message rather than hang until `go test` kills the binary.
func completesWithin(d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() { defer close(done); fn() }()

	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// -------------------------------------------------------------- the basics

func TestSubscribeAndPublish(t *testing.T) {
	bus := NewEventBus()
	subscriber := &countingSubscriber{}

	require.NoError(t, bus.Subscribe("user.created", subscriber))
	require.NoError(t, bus.Publish(context.Background(), "user.created"))

	assert.Equal(t, int64(1), subscriber.calls.Load())
}

func TestPublishToAnUnknownEventIsAnError(t *testing.T) {
	bus := NewEventBus()

	err := bus.Publish(context.Background(), "nobody.listening")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nobody.listening")
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	bus := NewEventBus()
	subscriber := &countingSubscriber{}

	require.NoError(t, bus.Subscribe("e", subscriber))
	require.NoError(t, bus.Publish(context.Background(), "e"))

	bus.Unsubscribe("e")

	assert.Error(t, bus.Publish(context.Background(), "e"))
	assert.Equal(t, int64(1), subscriber.calls.Load(), "no delivery after unsubscribing")
}

func TestAsyncSubscriberRunsAndWaitAsyncJoinsIt(t *testing.T) {
	bus := NewEventBus()
	subscriber := &countingSubscriber{}

	require.NoError(t, bus.SubscribeAsync("e", subscriber))
	require.NoError(t, bus.Publish(context.Background(), "e"))

	bus.WaitAsync()
	assert.Equal(t, int64(1), subscriber.calls.Load(),
		"WaitAsync must not return before the handler has finished")
}

func TestAPanickingAsyncSubscriberDoesNotKillTheProcess(t *testing.T) {
	bus := NewEventBus()
	subscriber := &panickingSubscriber{}

	require.NoError(t, bus.SubscribeAsync("e", subscriber))
	require.NoError(t, bus.Publish(context.Background(), "e"))

	require.True(t, completesWithin(5*time.Second, bus.WaitAsync),
		"WaitAsync must return even when the handler panicked — Done must still run")
	assert.True(t, subscriber.handled.Load())
}

// ----------------------------------------------------- the lock held on return

// TestANonPointerSubscriberDoesNotDeadlockTheBus is the reported deadlock. Both Subscribe
// forms took the mutex and then returned from inside the critical section on this path, so
// one bad subscriber wedged every later Subscribe, SubscribeAsync and Unsubscribe.
func TestANonPointerSubscriberDoesNotDeadlockTheBus(t *testing.T) {
	for name, subscribe := range map[string]func(core.IEventBus) error{
		"Subscribe": func(b core.IEventBus) error {
			return b.Subscribe("e", valueSubscriber{})
		},
		"SubscribeAsync": func(b core.IEventBus) error {
			return b.SubscribeAsync("e", valueSubscriber{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			bus := NewEventBus()

			err := subscribe(bus)
			require.Error(t, err, "a non-pointer subscriber must be rejected")
			assert.Contains(t, err.Error(), "pointer")

			// The bus must still work. This is what used to block forever.
			completed := completesWithin(5*time.Second, func() {
				_ = bus.Subscribe("ok", &countingSubscriber{})
				bus.Unsubscribe("ok")
			})
			require.True(t, completed,
				"the rejected subscribe left the mutex held, wedging the bus")
		})
	}
}

// TestANilSubscriberIsAnErrorNotAPanic: reflect.TypeOf(nil) returns nil, so the Kind() call
// dereferenced it.
func TestANilSubscriberIsAnErrorNotAPanic(t *testing.T) {
	bus := NewEventBus()

	var err error
	require.NotPanics(t, func() { err = bus.Subscribe("e", nil) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")

	require.NotPanics(t, func() { err = bus.SubscribeAsync("e", nil) })
	assert.Error(t, err)
}

// ------------------------------------------------------------------ the race

// TestPublishAndSubscribeConcurrently is the reported data race. Publish read the map with
// no lock while every write held one; the race detector reported event_bus.go:40 against
// :67. On a Go map that can escalate to `fatal error: concurrent map read and map write`,
// which RecoveryMiddleware cannot catch — it takes the process down, not the request.
//
// Run this package with -race for the assertion to mean anything.
func TestPublishAndSubscribeConcurrently(t *testing.T) {
	bus := NewEventBus()
	require.NoError(t, bus.Subscribe("e", &countingSubscriber{}))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); _ = bus.Publish(context.Background(), "e") }()
		go func(i int) {
			defer wg.Done()
			_ = bus.Subscribe(fmt.Sprintf("other-%d", i), &countingSubscriber{})
		}(i)
		go func(i int) {
			defer wg.Done()
			bus.Unsubscribe(fmt.Sprintf("other-%d", i))
		}(i)
	}

	require.True(t, completesWithin(30*time.Second, wg.Wait))
	bus.WaitAsync()
}

// TestConcurrentPublishesAreNotSerialised: the lock is released before the handler runs, so
// a slow handler must not block other publishes. Holding it across Handle is the obvious
// wrong fix for the race above.
func TestConcurrentPublishesAreNotSerialised(t *testing.T) {
	bus := NewEventBus()

	blocked := &countingSubscriber{block: make(chan struct{})}
	quick := &countingSubscriber{}

	require.NoError(t, bus.SubscribeAsync("slow", blocked))
	require.NoError(t, bus.Subscribe("quick", quick))

	require.NoError(t, bus.Publish(context.Background(), "slow"))

	// The slow handler is parked. A publish on another event must still get through.
	completed := completesWithin(5*time.Second, func() {
		_ = bus.Publish(context.Background(), "quick")
	})
	require.True(t, completed, "a parked handler must not block an unrelated publish")
	assert.Equal(t, int64(1), quick.calls.Load())

	close(blocked.block)
	bus.WaitAsync()
}

// TestASubscriberMaySubscribeFromInsideHandle: releasing the lock before dispatch is what
// makes this possible. Holding it across Handle would deadlock on the first attempt.
func TestASubscriberMaySubscribeFromInsideHandle(t *testing.T) {
	bus := NewEventBus()
	subscriber := &reentrantSubscriber{bus: bus}

	require.NoError(t, bus.Subscribe("outer", subscriber))

	completed := completesWithin(5*time.Second, func() {
		_ = bus.Publish(context.Background(), "outer")
	})
	require.True(t, completed, "a handler that subscribes must not deadlock")
	assert.NoError(t, subscriber.err)
}

// ------------------------------------------------------- the documented limit

// TestOneSubscriberPerEventName pins the contract, because "bus" invites the opposite
// assumption. Subscribing twice to one name replaces the first silently — this is a dispatch
// table, not fan-out. Pinned so the limitation is visible in the suite rather than
// discovered when the second subscriber never fires.
func TestOneSubscriberPerEventName(t *testing.T) {
	bus := NewEventBus()

	first := &countingSubscriber{}
	second := &countingSubscriber{}

	require.NoError(t, bus.Subscribe("e", first))
	require.NoError(t, bus.Subscribe("e", second))
	require.NoError(t, bus.Publish(context.Background(), "e"))

	assert.Equal(t, int64(0), first.calls.Load(), "the first subscriber is replaced")
	assert.Equal(t, int64(1), second.calls.Load())
}

// TestResubscribingCanChangeTheDeliveryMode, a consequence of the same replacement rule.
func TestResubscribingCanChangeTheDeliveryMode(t *testing.T) {
	bus := NewEventBus()
	subscriber := &countingSubscriber{}

	require.NoError(t, bus.Subscribe("e", subscriber))
	require.NoError(t, bus.SubscribeAsync("e", subscriber))
	require.NoError(t, bus.Publish(context.Background(), "e"))

	bus.WaitAsync()
	assert.Equal(t, int64(1), subscriber.calls.Load())
}
