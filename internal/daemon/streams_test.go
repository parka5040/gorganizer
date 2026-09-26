package daemon

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestStreamBusDeliversAndDropsWhenFull(t *testing.T) {
	_ = t.TempDir()
	bus := newStreamBus[int](1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := bus.Subscribe(ctx, "game")

	bus.Publish("game", 1)
	bus.Publish("game", 2)
	select {
	case got := <-ch:
		if got != 1 {
			t.Fatalf("event = %d, want 1", got)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive event")
	}
	select {
	case got := <-ch:
		t.Fatalf("event = %d, want full-buffer event to be dropped", got)
	default:
	}
}

func TestStreamBusConcurrentPublishAndUnsubscribe(t *testing.T) {
	_ = t.TempDir()
	bus := newStreamBus[int](4)
	start := make(chan struct{})
	stop := make(chan struct{})
	var publishers sync.WaitGroup
	for i := 0; i < 8; i++ {
		publishers.Add(1)
		go func() {
			defer publishers.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
					bus.Publish("game", 1)
					bus.PublishAll(2)
				}
			}
		}()
	}

	var subscribers sync.WaitGroup
	deadline := time.Now().Add(250 * time.Millisecond)
	for i := 0; i < 16; i++ {
		subscribers.Add(1)
		go func(worker int) {
			defer subscribers.Done()
			<-start
			for time.Now().Before(deadline) {
				ctx, cancel := context.WithCancel(context.Background())
				_, unsubscribe := bus.Subscribe(ctx, "game")
				if worker%2 == 0 {
					cancel()
				} else {
					unsubscribe()
					cancel()
				}
			}
		}(i)
	}

	close(start)
	subscribers.Wait()
	close(stop)
	publishers.Wait()
}
