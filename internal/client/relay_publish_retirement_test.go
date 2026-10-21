package client

import (
	"context"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

func TestPublishDoesNotWaitForRetiredRelayWorker(t *testing.T) {
	for _, name := range []string{"before_dispatch", "while_waiting", "ack_before_retirement"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			worker := &managedRelay{publish: make(chan relayPublish, 1), done: make(chan struct{})}
			peerDone := make(chan struct{})
			if name == "before_dispatch" {
				close(worker.done)
				close(peerDone)
			} else {
				go func() {
					defer close(peerDone)
					select {
					case request := <-worker.publish:
						if name == "ack_before_retirement" {
							request.result <- nil
						}
						close(worker.done)
					case <-ctx.Done():
					}
				}()
			}
			defer func() { cancel(); <-peerDone }()
			job := publishJob{key: transportTestKey, chat: "lobby", relays: []*managedRelay{worker},
				event: nostr.Event{Kind: ephChatKind, Content: "retirement race", Tags: nostr.Tags{{"d", "lobby"}}}}
			completed := make(chan publishResult, 1)
			go func() { completed <- executePublish(ctx, job) }()
			select {
			case result := <-completed:
				want := 0
				if name == "ack_before_retirement" {
					want = 1
				}
				if result.accepted != want {
					t.Fatalf("acknowledged = %d, want %d", result.accepted, want)
				}
				if name == "before_dispatch" && len(worker.publish) != 0 {
					t.Fatal("publication queued into an already retired worker")
				}
			case <-time.After(time.Second):
				cancel()
				<-completed
				t.Fatal("publication waited for the deadline instead of noticing worker retirement")
			}
		})
	}
}
