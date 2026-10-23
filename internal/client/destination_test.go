package client

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

// groupDestinationClient seeds an active mixed group (one named chat and one
// geohash chat) whose members are reachable through distinct inert relay
// workers. No worker is started, so nothing here touches the network.
func groupDestinationClient(t *testing.T, pow int) (*client, chan DisplayEvent) {
	t.Helper()
	c, events := testClient(t)
	for _, chat := range []string{"lobby", "u33dc"} {
		key := nostr.GeneratePrivateKey()
		pk, _ := nostr.GetPublicKey(key)
		c.chatKeys[chat] = chatSession{privKey: key, pubKey: pk, nick: "sender-" + chat}
	}
	c.config.Views = []View{
		{Name: "lobby"},
		{Name: "u33dc"},
		{Name: "group", IsGroup: true, Children: []string{"lobby", "u33dc"}, PoW: pow},
	}
	c.config.ActiveViewName = "group"
	c.relays["named"] = &managedRelay{url: "named", connected: true, chats: []string{"lobby"}}
	c.relays["geo"] = &managedRelay{url: "geo", connected: true, chats: []string{"u33dc"}}
	c.relays["elsewhere"] = &managedRelay{url: "elsewhere", connected: true, chats: []string{"other"}}
	return c, events
}

func nextJob(t *testing.T, c *client) publishJob {
	t.Helper()
	select {
	case job := <-c.outgoing:
		return job
	default:
		t.Fatal("message was not queued")
		return publishJob{}
	}
}

func expectNoJob(t *testing.T, c *client) {
	t.Helper()
	if len(c.outgoing) != 0 {
		t.Fatal("a refused send was queued")
	}
}

func onlyError(t *testing.T, events chan DisplayEvent) string {
	t.Helper()
	select {
	case event := <-events:
		if event.Type != "ERROR" {
			t.Fatalf("event type = %q, want ERROR", event.Type)
		}
		if len(events) != 0 {
			t.Fatalf("refusal emitted %d extra events", len(events))
		}
		return event.Content
	default:
		t.Fatal("refusal produced no error event")
		return ""
	}
}

func relayURLs(job publishJob) []string {
	var urls []string
	for _, worker := range job.relays {
		urls = append(urls, worker.url)
	}
	slices.Sort(urls)
	return urls
}

func TestSendToExplicitGroupDestinationSelectsWireFormatAndRelays(t *testing.T) {
	c, _ := groupDestinationClient(t, 4)

	cases := []struct {
		name     string
		chat     string
		kind     int
		tag      string
		wantURLs []string
	}{
		{name: "named", chat: "lobby", kind: ephChatKind, tag: "d", wantURLs: []string{"named"}},
		{name: "geohash", chat: "u33dc", kind: geoChatKind, tag: "g", wantURLs: []string{"geo"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c.handleAction(UserAction{Type: "SEND_MESSAGE", Payload: "hello group", View: "group", Chat: tt.chat})
			job := nextJob(t, c)
			if job.chat != tt.chat {
				t.Fatalf("job chat = %q, want %q", job.chat, tt.chat)
			}
			if job.event.Kind != tt.kind || job.event.Tags.Find(tt.tag)[1] != tt.chat {
				t.Fatalf("destination tag/kind = %v/%d, want %s/%d", job.event.Tags, job.event.Kind, tt.tag, tt.kind)
			}
			session := c.chatKeys[tt.chat]
			if job.key != session.privKey || job.event.PubKey != session.pubKey || job.event.Tags.Find("n")[1] != session.nick {
				t.Fatal("group send did not use the destination chat's session identity")
			}
			if job.event.Content != "hello group" {
				t.Fatalf("content = %q, want exact payload", job.event.Content)
			}
			if job.difficulty != 4 {
				t.Fatalf("difficulty = %d, want active group PoW 4", job.difficulty)
			}
			if tag := job.event.Tags.Find("p"); tag != nil {
				t.Fatalf("destination-only send added a recipient tag: %v", tag)
			}
			if got := relayURLs(job); !slices.Equal(got, tt.wantURLs) {
				t.Fatalf("relays = %v, want %v", got, tt.wantURLs)
			}
		})
	}
}

func TestSendRejectsInvalidDestinationContext(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		view    string
		chat    string
		want    string
	}{
		{name: "stale-view-with-live-default", payload: "hello", view: "other", chat: "lobby", want: "active chat is now 'group'"},
		// @alice#beef resolves inside the group, so the refusal proves the
		// invalid default is validated before the mention can override it.
		{name: "invalid-default-despite-mention", payload: "@alice#beef hello", view: "group", chat: "other", want: "Selected recipient is not in the active chat/group."},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c, events := groupDestinationClient(t, 0)
			c.config.Views = append(c.config.Views, View{Name: "other"})
			c.userContext.Add(strings.Repeat("a", 60)+"beef", userContext{nick: "alice", shortPubKey: "beef", chat: "lobby"})
			c.handleAction(UserAction{Type: "SEND_MESSAGE", Payload: tt.payload, View: tt.view, Chat: tt.chat})
			expectNoJob(t, c)
			if got := onlyError(t, events); !strings.Contains(got, tt.want) {
				t.Fatalf("error = %q, want substring %q", got, tt.want)
			}
		})
	}
}

func TestMentionOverridesExplicitDestination(t *testing.T) {
	fullKey := strings.Repeat("b", 60) + "cafe"

	t.Run("valid-mention", func(t *testing.T) {
		c, _ := groupDestinationClient(t, 0)
		c.userContext.Add(fullKey, userContext{nick: "alice", shortPubKey: "cafe", chat: "u33dc"})
		c.handleAction(UserAction{Type: "SEND_MESSAGE", Payload: "@alice#cafe hello", View: "group", Chat: "lobby"})
		job := nextJob(t, c)
		if job.chat != "u33dc" || job.event.Kind != geoChatKind || job.event.Tags.Find("g")[1] != "u33dc" {
			t.Fatal("mention did not override the explicit destination")
		}
		if session := c.chatKeys["u33dc"]; job.key != session.privKey || job.event.PubKey != session.pubKey || job.event.Tags.Find("n")[1] != session.nick {
			t.Fatal("mention did not select the overridden chat's sender")
		}
		if tag := job.event.Tags.Find("p"); tag == nil || tag[1] != fullKey {
			t.Fatalf("recipient tag = %v, want %s", tag, fullKey)
		}
		if got := relayURLs(job); !slices.Equal(got, []string{"geo"}) {
			t.Fatalf("relays = %v, want the overridden chat's relay", got)
		}
	})

	t.Run("full-key-mention-wins-over-short-handle", func(t *testing.T) {
		c, _ := groupDestinationClient(t, 0)
		c.userContext.Add(strings.Repeat("a", 60)+"beef", userContext{nick: "alice", shortPubKey: "beef", chat: "lobby"})
		c.userContext.Add(fullKey, userContext{nick: "alice", shortPubKey: "cafe", chat: "u33dc"})
		c.handleAction(UserAction{Type: "SEND_MESSAGE", Payload: "@alice#" + fullKey + " hello", View: "group", Chat: "lobby"})
		job := nextJob(t, c)
		if job.chat != "u33dc" || job.event.Tags.Find("p")[1] != fullKey {
			t.Fatal("full-key mention did not select its own recipient")
		}
	})

	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "unknown", payload: "@bob#beef hello", want: "Unknown recipient"},
		{name: "incomplete-token", payload: "@alice#ca hello", want: "Unknown recipient"},
		// A cached user in another view must not be reachable by mention.
		{name: "outside-active-view", payload: "@dave#f00d hello", want: "Unknown recipient"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c, events := groupDestinationClient(t, 0)
			c.userContext.Add(fullKey, userContext{nick: "alice", shortPubKey: "cafe", chat: "u33dc"})
			c.userContext.Add(strings.Repeat("d", 60)+"f00d", userContext{nick: "dave", shortPubKey: "f00d", chat: "other"})
			c.handleAction(UserAction{Type: "SEND_MESSAGE", Payload: tt.payload, View: "group", Chat: "lobby"})
			expectNoJob(t, c)
			if got := onlyError(t, events); !strings.Contains(got, tt.want) {
				t.Fatalf("error = %q, want substring %q", got, tt.want)
			}
		})
	}

	t.Run("ambiguous-mention", func(t *testing.T) {
		c, events := groupDestinationClient(t, 0)
		c.userContext.Add(strings.Repeat("a", 60)+"beef", userContext{nick: "alice", shortPubKey: "beef", chat: "lobby"})
		c.userContext.Add(strings.Repeat("b", 60)+"beef", userContext{nick: "alice", shortPubKey: "beef", chat: "u33dc"})
		c.handleAction(UserAction{Type: "SEND_MESSAGE", Payload: "@alice#beef hello", View: "group", Chat: "lobby"})
		expectNoJob(t, c)
		if got := onlyError(t, events); !strings.Contains(got, "Ambiguous") {
			t.Fatalf("error = %q, want ambiguity refusal", got)
		}
	})
}

// previewScenarioClient seeds one destination scenario. Preview and send must
// agree, so every case is built twice from the same seed.
func previewScenarioClient(t *testing.T, active string) (*client, chan DisplayEvent) {
	t.Helper()
	c, events := groupDestinationClient(t, 0)
	c.config.Views = append(c.config.Views, View{Name: "other"})
	c.config.ActiveViewName = active
	c.userContext.Add(strings.Repeat("a", 60)+"beef", userContext{nick: "alice", shortPubKey: "beef", chat: "lobby"})
	return c, events
}

func TestResolveMessageTargetPreviewMatchesSendWithoutSideEffects(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		view     string
		chat     string
		active   string
		wantChat string
		wantErr  string
	}{
		{name: "group-default", text: "hello", view: "group", chat: "lobby", active: "group", wantChat: "lobby"},
		{name: "group-mention-overrides", text: "@alice#beef hi", view: "group", chat: "u33dc", active: "group", wantChat: "lobby"},
		{name: "group-no-destination", text: "hello", view: "group", active: "group", wantErr: "Select a chat or use @nick"},
		{name: "stale-view", text: "hello", view: "other", active: "group", wantErr: "active chat is now 'group'"},
		{name: "chat-without-view", text: "hello", chat: "lobby", active: "group", wantErr: "requires the active chat context"},
		{name: "invalid-default", text: "hello", view: "group", chat: "other", active: "group", wantErr: "Selected recipient"},
		{name: "ordinary-live-context", text: "hello", active: "lobby", wantChat: "lobby"},
		{name: "ordinary-explicit", text: "hello", view: "lobby", chat: "lobby", active: "lobby", wantChat: "lobby"},
		{name: "ordinary-other-chat", text: "hello", view: "lobby", chat: "other", active: "lobby", wantErr: "Selected recipient"},
		{name: "unknown-mention", text: "@bob#beef hi", view: "group", chat: "lobby", active: "group", wantErr: "Unknown recipient"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			action := UserAction{Type: "RESOLVE_MESSAGE_TARGET", Payload: tt.text, View: tt.view, Chat: tt.chat}

			c, events := previewScenarioClient(t, tt.active)
			before := *c.config
			views := slices.Clone(c.config.Views)
			keys := make(map[string]chatSession, len(c.chatKeys))
			for name, session := range c.chatKeys {
				keys[name] = session
			}
			relays := len(c.relays)

			c.handleAction(action)

			if len(events) != 1 {
				t.Fatalf("preview emitted %d events, want exactly one", len(events))
			}
			event := <-events
			if event.Type != "MESSAGE_TARGET" {
				t.Fatalf("preview event type = %q, want MESSAGE_TARGET", event.Type)
			}
			target, ok := event.Payload.(MessageTarget)
			if !ok {
				t.Fatalf("preview payload type = %T, want MessageTarget", event.Payload)
			}
			if target.Text != tt.text || target.View != tt.view || target.DefaultChat != tt.chat {
				t.Fatalf("preview did not echo inputs: %+v", target)
			}
			if (target.Chat == "") == (target.Error == "") {
				t.Fatalf("target must carry exactly one of chat or error: %+v", target)
			}
			if target.Chat != tt.wantChat {
				t.Fatalf("preview chat = %q, want %q", target.Chat, tt.wantChat)
			}
			if tt.wantErr != "" && !strings.Contains(target.Error, tt.wantErr) {
				t.Fatalf("preview error = %q, want substring %q", target.Error, tt.wantErr)
			}
			// A preview must not queue, generate identities, save config or
			// touch relay/subscription state.
			expectNoJob(t, c)
			if !reflect.DeepEqual(before, *c.config) || !reflect.DeepEqual(views, c.config.Views) {
				t.Fatal("preview mutated configuration")
			}
			if !reflect.DeepEqual(keys, c.chatKeys) {
				t.Fatal("preview generated or changed a chat identity")
			}
			if len(c.relays) != relays {
				t.Fatal("preview changed the relay set")
			}

			// The send path must reach the same destination decision.
			sendClient, sendEvents := previewScenarioClient(t, tt.active)
			sendClient.handleAction(UserAction{Type: "SEND_MESSAGE", Payload: tt.text, View: tt.view, Chat: tt.chat})
			if target.Error != "" {
				expectNoJob(t, sendClient)
				if got := onlyError(t, sendEvents); got != target.Error {
					t.Fatalf("send error %q disagrees with preview %q", got, target.Error)
				}
				return
			}
			if job := nextJob(t, sendClient); job.chat != target.Chat {
				t.Fatalf("send chat = %q disagrees with preview %q", job.chat, target.Chat)
			}
		})
	}
}

func TestQueuedExplicitDestinationIsImmutableAfterFocusChange(t *testing.T) {
	c, _ := groupDestinationClient(t, 3)
	c.userContext.Add(strings.Repeat("b", 60)+"cafe", userContext{nick: "alice", shortPubKey: "cafe", chat: "u33dc"})
	c.handleAction(UserAction{Type: "SEND_MESSAGE", Payload: "@alice#cafe hello", View: "group", Chat: "lobby"})
	job := nextJob(t, c)

	session := c.chatKeys["u33dc"]
	// Everything the TUI could legitimately change afterwards: another view,
	// a group that lost the member, PoW cleared, a new chat identity and dead
	// relays.
	c.config.ActiveViewName = "lobby"
	c.config.Views[2].Children = []string{"lobby"}
	c.config.Views[2].PoW = 0
	c.chatKeys["u33dc"] = chatSession{privKey: strings.Repeat("f", 64), pubKey: strings.Repeat("f", 64), nick: "mallory"}
	c.relays["geo"].connected = false
	c.relays["named"].connected = false
	c.userContext.Purge()

	if job.chat != "u33dc" || job.event.Tags.Find("g")[1] != "u33dc" {
		t.Fatal("queued job changed its destination with the active view")
	}
	if job.key != session.privKey || job.event.PubKey != session.pubKey {
		t.Fatal("queued job changed its signing identity")
	}
	if tag := job.event.Tags.Find("p"); tag == nil || tag[1] != strings.Repeat("b", 60)+"cafe" {
		t.Fatal("queued job lost its mention recipient")
	}
	if job.difficulty != 3 {
		t.Fatalf("queued difficulty = %d, want the PoW captured at send time", job.difficulty)
	}
	if got := relayURLs(job); !slices.Equal(got, []string{"geo"}) {
		t.Fatalf("queued relays = %v, want the captured destination relay", got)
	}
}

func TestSendAfterViewChangeRejectsCapturedComposerContext(t *testing.T) {
	c, events := groupDestinationClient(t, 0)
	// The composer captured the group, then the user switched to another chat.
	c.config.ActiveViewName = "lobby"
	c.handleAction(UserAction{Type: "SEND_MESSAGE", Payload: "hello", View: "group", Chat: "u33dc"})
	expectNoJob(t, c)
	got := onlyError(t, events)
	if !strings.Contains(got, "composed for 'group'") || !strings.Contains(got, "now 'lobby'") {
		t.Fatalf("error = %q, want stale-context refusal", got)
	}
}

func TestDestinationContextWithoutActiveChat(t *testing.T) {
	for _, actionType := range []string{"SEND_MESSAGE", "RESOLVE_MESSAGE_TARGET"} {
		t.Run(actionType, func(t *testing.T) {
			c, events := testClient(t)
			c.config.Views = nil
			c.config.ActiveViewName = ""
			c.handleAction(UserAction{Type: actionType, Payload: "hello", View: "group", Chat: "lobby"})
			expectNoJob(t, c)
			if actionType == "RESOLVE_MESSAGE_TARGET" {
				event := <-events
				target, ok := event.Payload.(MessageTarget)
				if event.Type != "MESSAGE_TARGET" || !ok || !strings.Contains(target.Error, "No active chat") {
					t.Fatalf("preview without an active chat = %+v", event)
				}
				return
			}
			if got := onlyError(t, events); !strings.Contains(got, "No active chat") {
				t.Fatalf("error = %q, want no-active-chat refusal", got)
			}
		})
	}
}
