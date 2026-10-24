package events

import (
	"context"
	"testing"
)

func TestHubPublishesAndCancelsSubscribers(t *testing.T) {
	hub := NewHub()
	channel, cancel := hub.Subscribe()
	hub.Publish(Event{Target: TargetCatalog})
	select {
	case event := <-channel:
		if event.Target != TargetCatalog {
			t.Fatalf("target=%q, want %q", event.Target, TargetCatalog)
		}
	default:
		t.Fatal("subscriber did not receive published event")
	}
	cancel()
	hub.Publish(Event{Target: TargetRSS})
	select {
	case event := <-channel:
		t.Fatalf("cancelled subscriber received event %q", event.Target)
	default:
	}
}

func TestHubPublishDoesNotBlockOnSlowSubscriber(t *testing.T) {
	hub := NewHub()
	_, cancel := hub.Subscribe()
	defer cancel()
	for i := 0; i < 32; i++ {
		hub.Publish(Event{Target: TargetRecords})
	}
}

func TestAfterCommitPublishesBeforeExistingHook(t *testing.T) {
	hub := NewHub()
	channel, cancel := hub.Subscribe()
	defer cancel()
	called := false
	hook := hub.AfterCommit(TargetSubscribed, func(context.Context) { called = true })
	hook(context.Background())
	select {
	case event := <-channel:
		if event.Target != TargetSubscribed {
			t.Fatalf("target=%q, want %q", event.Target, TargetSubscribed)
		}
	default:
		t.Fatal("after-commit hook did not publish event")
	}
	if !called {
		t.Fatal("existing after-commit hook was not called")
	}
}
