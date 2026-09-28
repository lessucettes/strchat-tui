package client

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

// Synthetic signed fixtures following the pinned Swift/Android public builders,
// not captured traffic and not evidence of executing either mobile application.
func TestBitChatPublicWireShapes(t *testing.T) {
	for _, variant := range []string{"android-unmined", "swift-hex-nonce", "android-decimal-nonce", "nickname-absent", "teleported"} {
		t.Run(variant, func(t *testing.T) {
			c, events := testClient(t)
			c.config.Views = []View{{Name: "u33dc"}}
			c.config.ActiveViewName = "u33dc"
			key := strings.Repeat("0", 63) + "1"
			pk, _ := nostr.GetPublicKey(key)
			event := nostr.Event{Kind: 20000, PubKey: pk, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"g", "u33dc"}}, Content: "hello from a public geohash channel"}
			if variant != "nickname-absent" {
				event.Tags = append(event.Tags, nostr.Tag{"n", "alice"})
			}
			if variant == "teleported" {
				event.Tags = append(event.Tags, nostr.Tag{"t", "teleport"})
			}
			if strings.Contains(variant, "nonce") {
				event.Tags = append(event.Tags, nostr.Tag{"nonce", "0", "8"})
				for nonce := 0; ; nonce++ {
					value := fmt.Sprint(nonce)
					if variant == "swift-hex-nonce" {
						value = fmt.Sprintf("%016x", nonce)
					}
					event.Tags[len(event.Tags)-1][1] = value
					if countLeadingZeroBits(event.GetID()) >= 8 {
						break
					}
				}
			}
			if err := event.Sign(key); err != nil {
				t.Fatal(err)
			}
			c.processEvent(&event, "fixture")
			select {
			case displayed := <-events:
				if displayed.Chat != "u33dc" || displayed.Content != event.Content || displayed.Type != "NEW_MESSAGE" {
					t.Fatal("wire shape was not displayed correctly")
				}
			default:
				t.Fatalf("valid %s message rejected", variant)
			}
		})
	}
}

func TestGeohashPublishKeepsPublicWireFormat(t *testing.T) {
	c, _ := testClient(t)
	c.config.Views = []View{{Name: "u33dc"}}
	c.config.ActiveViewName = "u33dc"
	key := strings.Repeat("0", 63) + "1"
	pk, _ := nostr.GetPublicKey(key)
	c.chatKeys["u33dc"] = chatSession{privKey: key, pubKey: pk, nick: "alice"}
	c.relays["fixture"] = &managedRelay{connected: true, chats: []string{"u33dc"}}
	c.publishMessage("hello")
	select {
	case job := <-c.outgoing:
		if job.event.Kind != 20000 || job.event.Tags.Find("g")[1] != "u33dc" || job.event.Tags.Find("n")[1] != "alice" {
			t.Fatal("public geohash format changed")
		}
		if job.difficulty != 0 {
			t.Fatal("PoW was enabled without user choice")
		}
	default:
		t.Fatal("geohash message was not queued")
	}
}
