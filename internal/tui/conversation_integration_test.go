package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gdamore/tcell/v2"
	"github.com/nbd-wtf/go-nostr"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// Exercise the real UI -> owner -> signed wire event path on a loopback relay.
// Both members are named chats: geographic wire tags are covered in client tests
// with inert relay workers, without opening the embedded public geo catalogs.
func TestGroupConversationEndToEndLoopback(t *testing.T) {
	peerKey := nostr.GeneratePrivateKey()
	published := make(chan nostr.Event, 8)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		write := func(value any) bool {
			data, err := json.Marshal(value)
			return err == nil && conn.Write(ctx, websocket.MessageText, data) == nil
		}
		var subscription string
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var frame []json.RawMessage
			if json.Unmarshal(data, &frame) != nil || len(frame) < 2 {
				continue
			}
			var kind string
			if json.Unmarshal(frame[0], &kind) != nil {
				continue
			}
			switch kind {
			case "REQ":
				requests.Add(1)
				if json.Unmarshal(frame[1], &subscription) != nil {
					return
				}
				for _, chat := range []string{"coffee", "lobby"} {
					ev := nostr.Event{CreatedAt: nostr.Now(), Kind: 23333, Content: "peer in " + chat,
						Tags: nostr.Tags{{"d", chat}, {"n", "peer"}}}
					if ev.Sign(peerKey) != nil || !write([]any{"EVENT", subscription, ev}) {
						return
					}
				}
				if !write([]any{"EOSE", subscription}) {
					return
				}
			case "EVENT":
				var ev nostr.Event
				if json.Unmarshal(frame[1], &ev) != nil {
					continue
				}
				select {
				case published <- ev:
				case <-ctx.Done():
					return
				}
				if !write([]any{"OK", ev.ID, true, ""}) || !write([]any{"EVENT", subscription, ev}) {
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("APPDATA", home)
	configHome, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(configHome, "strchat-tui")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"nick": "me", "active_view_name": "mixed",
		"anchor_relays": []string{"ws" + strings.TrimPrefix(server.URL, "http")},
		"views":         []client.View{{Name: "mixed", IsGroup: true, Children: []string{"coffee", "lobby"}}, {Name: "coffee"}, {Name: "lobby"}},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	s := newSimTUI(t, 80, 32)
	owner, err := client.New(s.actions, s.events)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { owner.Run(); close(done) }()
	t.Cleanup(func() {
		owner.Stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("client did not stop")
		}
	})
	waitFor(t, 5*time.Second, func() bool {
		return strings.Contains(s.ui.modelOutputText(), "peer in lobby")
	})
	baseline := requests.Load()
	s.onLoop(func() {
		s.ui.render()
		s.ui.setFocus(s.ui.input)
		s.ui.input.SetText("plain message")
	})
	s.key(tcell.KeyEnter, 0, tcell.ModNone)
	var coffeePK string
	select {
	case event := <-published:
		coffeePK = event.PubKey
		if event.Kind != 23333 || event.Tags.Find("d")[1] != "coffee" || event.Content != "plain message" {
			t.Fatalf("wrong published message: kind %d tags %v content %q", event.Kind, event.Tags, event.Content)
		}
		if ok, err := event.CheckSignature(); err != nil || !ok {
			t.Fatalf("wire event is not signed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("plain group message never reached the loopback relay")
	}
	enterText(s, "/set mixed lobby")
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return s.ui.conversationChat(s.ui.snapshot()) == "lobby" })
	})
	s.onLoop(func() { s.ui.input.SetText("command destination") })
	s.key(tcell.KeyEnter, 0, tcell.ModNone)
	select {
	case event := <-published:
		if event.Tags.Find("d")[1] != "lobby" || event.Content != "command destination" || len(event.Tags.Find("p")) != 0 || event.PubKey == coffeePK {
			t.Fatal("command-selected destination did not control the signed ordinary send")
		}
		if ok, err := event.CheckSignature(); err != nil || !ok {
			t.Fatalf("command-selected event is not signed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command-selected message never reached the loopback relay")
	}
	enterText(s, "/s mixed coffee")
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return s.ui.conversationChat(s.ui.snapshot()) == "coffee" })
	})
	peerPK, _ := nostr.GetPublicKey(peerKey)
	s.onLoop(func() { s.ui.input.SetText("@peer#" + peerPK + " reply") })
	t.Cleanup(func() {
		if t.Failed() {
			s.onLoop(func() {
				t.Logf("composer text=%q title=%q query=%+v preview=%+v recipientRev=%d", s.ui.input.GetText(), s.ui.input.GetTitle(), s.ui.targetQuery, s.ui.snapshot().messageTarget, s.ui.snapshot().recipientRev)
			})
		}
	})
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool {
			target, ready := s.ui.currentMessageTarget(s.ui.snapshot())
			return ready && target.Error == "" && target.Chat == "lobby"
		})
	})
	s.key(tcell.KeyEnter, 0, tcell.ModNone)
	select {
	case event := <-published:
		if event.Tags.Find("d")[1] != "lobby" || event.Tags.Find("p")[1] != peerPK {
			t.Fatal("mention did not override the focused coffee destination")
		}
		if event.PubKey == coffeePK {
			t.Fatal("different group members shared a signing identity")
		}
		if ok, err := event.CheckSignature(); err != nil || !ok {
			t.Fatalf("mention event is not signed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("group mention never reached the loopback relay")
	}
	if got := requests.Load(); got != baseline {
		t.Fatalf("conversation selection changed subscriptions: %d -> %d", baseline, got)
	}
	enterText(s, "/set coffee")
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { view := activeView(s.ui.snapshot()); return view != nil && view.Name == "coffee" })
	})
	s.onLoop(func() { s.ui.input.SetText("direct coffee") })
	s.key(tcell.KeyEnter, 0, tcell.ModNone)
	select {
	case event := <-published:
		if event.PubKey != coffeePK || event.Tags.Find("d")[1] != "coffee" || event.Content != "direct coffee" {
			t.Fatal("opening the chat separately changed the wire sender")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("direct chat message never reached the loopback relay")
	}
	enterText(s, "/set mixed lobby")
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool {
			view := activeView(s.ui.snapshot())
			return view != nil && view.Name == "mixed" && s.ui.conversationChat(s.ui.snapshot()) == "lobby"
		})
	})
	data, err = os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		ActiveViewName string        `json:"active_view_name"`
		Views          []client.View `json:"views"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ActiveViewName != "mixed" || len(saved.Views) == 0 || saved.Views[0].Name != "mixed" || saved.Views[0].SelectedChat != "lobby" {
		t.Fatal("command-selected group and destination were not persisted")
	}
}
