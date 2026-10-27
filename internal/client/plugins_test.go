package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

// Re-exec the test binary: portable executables, no shell or external services.
func TestMessagePluginProcess(t *testing.T) {
	args := os.Args
	for len(args) > 0 && args[0] != "--message-plugin" {
		args = args[1:]
	}
	if len(args) == 0 {
		return
	}
	var request json.RawMessage
	if json.NewDecoder(os.Stdin).Decode(&request) != nil {
		os.Exit(2)
	}
	mode := args[1]
	if mode == "gate" {
		if os.WriteFile(args[2], []byte("ready"), 0600) != nil {
			os.Exit(4)
		}
		for {
			if _, err := os.Stat(args[3]); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		mode = "replace"
		if len(args) > 4 {
			mode = args[4]
		}
	}
	switch mode {
	case "request":
		json.NewEncoder(os.Stdout).Encode(map[string]any{"version": 1, "action": "replace", "content": string(request)})
	case "replace":
		// Attempts to replace read-only fields must have no effect.
		json.NewEncoder(os.Stdout).Encode(map[string]any{"version": 1, "action": "replace", "content": "translated", "chat": "elsewhere", "sender": map[string]string{"pubkey": "forged"}})
	case "suppress", "unchanged":
		json.NewEncoder(os.Stdout).Encode(map[string]any{"version": 1, "action": mode})
	case "oversized":
		json.NewEncoder(os.Stdout).Encode(map[string]any{"version": 1, "action": "replace", "content": strings.Repeat("x", MaxMsgLen+1)})
	case "fail":
		fmt.Fprintln(os.Stderr, "private diagnostic must not enter client logs")
		os.Exit(3)
	case "malformed":
		fmt.Print(`{"version":1,"action":"replace"}`)
	case "trailing":
		fmt.Print("{\"version\":1,\"action\":\"unchanged\"}\n{}\n")
	case "huge":
		fmt.Print(strings.Repeat("x", maxPluginOutputBytes+1))
	case "wait":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

func testPlugin(t *testing.T, mode string) *pluginConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &pluginConfig{Command: []string{exe, "-test.run=^TestMessagePluginProcess$", "--", "--message-plugin", mode}, Timeout: 3 * time.Second}
}

func TestExecMessagePlugin(t *testing.T) {
	request := pluginRequest{Version: 1, Plugin: "on_send", Content: "original"}
	for _, mode := range []string{"replace", "suppress", "unchanged", "fail", "malformed", "trailing", "huge"} {
		t.Run(mode, func(t *testing.T) {
			content, suppress, err := runMessagePlugin(context.Background(), testPlugin(t, mode), request)
			switch mode {
			case "replace":
				if err != nil || suppress || content != "translated" {
					t.Fatalf("replacement: content=%q suppress=%v err=%v", content, suppress, err)
				}
			case "suppress":
				if err != nil || !suppress {
					t.Fatalf("suppression: suppress=%v err=%v", suppress, err)
				}
			case "unchanged":
				if err != nil || suppress || content != request.Content {
					t.Fatalf("unchanged: content=%q suppress=%v err=%v", content, suppress, err)
				}
			default:
				if err == nil || strings.Contains(err.Error(), "private diagnostic") {
					t.Fatalf("expected safe error: %v", err)
				}
			}
		})
	}
}

func TestPluginOptionsAreRuntimeOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("APPDATA", home)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := getAppConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Previously written or manually added plugin fields must never enable code.
	writeTestFile(t, filepath.Join(dir, "config.json"), `{"plugins":{"on_send":{"command":["must-not-run-from-disk"]}}}`, 0600)
	c, err := NewWithOptions(nil, nil, Options{PluginSend: executable, PluginReceive: executable, PluginTimeout: 35 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.cancel)
	for _, plugin := range []*pluginConfig{c.plugins.OnSend, c.plugins.OnReceive} {
		if plugin == nil || len(plugin.Command) != 1 || plugin.Command[0] != executable || plugin.Timeout != 35*time.Second {
			t.Fatal("runtime plugin options were not installed")
		}
	}
	if err := c.config.save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(c.config.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "plugin") || strings.Contains(string(data), executable) {
		t.Fatal("plugin options leaked into saved configuration")
	}
	without, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(without.cancel)
	if without.plugins.OnSend != nil || without.plugins.OnReceive != nil {
		t.Fatal("plugins survived a launch without runtime options")
	}
}

func TestOutgoingPluginsFailClosedAndKeepCapturedIdentity(t *testing.T) {
	for _, mode := range []string{"replace", "request", "suppress", "fail", "malformed", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := groupDestinationClient(t, 3)
			c.plugins = messagePlugins{OnSend: testPlugin(t, mode)}
			worker := c.relays["geo"]
			worker.publish = make(chan relayPublish, 1)
			recipient := strings.Repeat("a", 60) + "beef"
			c.userContext.Add(recipient, userContext{nick: "alice", shortPubKey: "beef", chat: "u33dc"})
			message := "@alice#beef original"
			c.publishMessageInView(message, "group", "lobby", "")
			job := nextJob(t, c)
			pubkey := c.chatKeys["u33dc"].pubKey
			c.config.ActiveViewName = "lobby"
			delete(c.chatKeys, "u33dc")
			done := make(chan publishResult, 1)
			go func() { done <- executePublish(c.ctx, job) }()
			publishes := mode == "replace" || mode == "request"
			if publishes {
				select {
				case request := <-worker.publish:
					e := request.event
					_, _, valid := verifyEventDigestWarm(&e, nil, nil)
					if !valid || !isPoWValid(&e, 3) || e.PubKey.Hex() != pubkey || e.Kind != geoChatKind || e.Tags.Find("g")[1] != "u33dc" || e.Tags.Find("p")[1] != recipient {
						t.Errorf("transformed publication lost signature, identity or destination: %+v", e)
					}
					if mode == "request" {
						assertPluginEnvelope(t, e.Content, map[string]any{
							"version": float64(1), "plugin": "on_send", "content": message,
							"sender": map[string]any{"pubkey": pubkey, "nick": "sender-u33dc"},
						})
					} else if e.Content != "translated" {
						t.Error("outgoing content not transformed")
					}
					request.result <- nil
				case <-time.After(5 * time.Second):
					t.Fatal("no transformed publication")
				}
			}
			select {
			case result := <-done:
				if publishes && result.accepted != 1 {
					t.Fatalf("send failed: %+v", result)
				}
				if !publishes && (result.accepted != 0 || len(worker.publish) != 0) {
					t.Fatal("failed/suppressed plugin published a message")
				}
				if !publishes && mode != "suppress" && result.kind != "ERROR" {
					t.Fatalf("missing send failure: %+v", result)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("send did not finish")
			}
		})
	}
}

func assertPluginEnvelope(t *testing.T, text string, want map[string]any) {
	t.Helper()
	var got map[string]any
	// Match an independent allowlist, including absence of signing material.
	if err := json.Unmarshal([]byte(text), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("unexpected plugin envelope fields or values: %v", err)
	}
}

func TestIncomingPluginsOnlyChangeValidatedPresentation(t *testing.T) {
	for _, mode := range []string{"replace", "request", "suppress", "fail"} {
		t.Run(mode, func(t *testing.T) {
			c, events := testClient(t)
			c.plugins = messagePlugins{OnReceive: testPlugin(t, mode)}
			c.receiveJobs = make(chan receivePluginJob, 32)
			c.receiveResults = make(chan receivePluginResult, 32)
			c.wg.Go(c.receivePluginLoop)
			defer c.shutdown()
			original := signedEvent(t, "original")
			bad := *original
			bad.Sig = [64]byte{}
			c.processEvent(&bad, "local")
			if len(c.receiveJobs) != 0 || len(events) != 0 {
				t.Fatal("unverified event reached plugin/display")
			}
			c.processEvent(original, "local")
			c.processEvent(original, "duplicate")
			select {
			case result := <-c.receiveResults:
				c.finishReceivePlugin(result)
			case <-time.After(5 * time.Second):
				t.Fatal("incoming plugin did not finish")
			}
			want := "translated"
			if mode == "fail" {
				want = "original"
			}
			messages, warnings := 0, 0
			for len(events) > 0 {
				e := <-events
				if e.Type == "NEW_MESSAGE" {
					messages++
					if (mode != "request" && e.Content != want) || e.Chat != "lobby" || e.FullPubKey != original.PubKey.Hex() {
						t.Fatalf("wrong local presentation: %+v", e)
					}
					if mode == "request" {
						assertPluginEnvelope(t, e.Content, map[string]any{
							"version": float64(1), "plugin": "on_receive", "content": "original",
							"sender":   map[string]any{"pubkey": original.PubKey.Hex(), "nick": "alice"},
							"event_id": original.ID.Hex(), "created_at": float64(original.CreatedAt),
						})
					}
				}
				if e.Type == "WARN" {
					warnings++
				}
			}
			if (mode == "suppress" && messages != 0) || (mode != "suppress" && messages != 1) || (mode == "fail" && warnings != 1) {
				t.Fatalf("messages=%d warnings=%d mode=%s", messages, warnings, mode)
			}
			if original.Content != "original" || len(c.receiveJobs) != 0 {
				t.Fatal("plugin mutated the signed event or duplicate was queued")
			}
		})
	}
}

func TestIncomingPluginSkipsOwnMessages(t *testing.T) {
	for _, tt := range []struct {
		name, source string
		retired      bool
	}{
		{"local_feedback", "local", false},
		{"relay_echo", "relay", false},
		{"retired_identity", "relay", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, events := testClient(t)
			c.plugins.OnReceive = testPlugin(t, "replace")
			c.receiveJobs = make(chan receivePluginJob, 2)
			own := signedEvent(t, "Bonjour !")
			if tt.retired {
				c.retiredKeys.Add(own.PubKey.Hex(), true)
			} else {
				c.chatKeys["lobby"] = chatSession{pubKey: own.PubKey.Hex()}
			}
			c.processEvent(own, tt.source)
			if len(c.receiveJobs) != 0 || len(events) != 1 {
				t.Fatal("own message must display without entering the receive plugin queue")
			}
			display := <-events
			if display.Type != "NEW_MESSAGE" || !display.IsOwnMessage || display.Content != own.Content {
				t.Fatalf("own message did not retain its published content: %+v", display)
			}
			c.processEvent(own, "duplicate")
			if len(events) != 0 || len(c.receiveJobs) != 0 {
				t.Fatal("own echo was displayed or processed twice")
			}

			foreign := signedEvent(t, "foreign original")
			key := nostr.Generate()
			foreign.PubKey = key.Public()
			if err := foreign.Sign(key); err != nil {
				t.Fatal(err)
			}
			c.processEvent(foreign, tt.source)
			if len(c.receiveJobs) != 1 || len(events) != 0 {
				t.Fatal("foreign message must still pass through the receive plugin")
			}
			job := <-c.receiveJobs
			content, suppress, err := runMessagePlugin(c.ctx, job.plugin, job.request)
			c.finishReceivePlugin(receivePluginResult{job: job, content: content, suppress: suppress, err: err})
			if err != nil || len(events) != 1 {
				t.Fatalf("foreign message processing failed: %v", err)
			}
			display = <-events
			if display.Type != "NEW_MESSAGE" || display.IsOwnMessage || display.Content != "translated" || foreign.Content != "foreign original" {
				t.Fatalf("foreign message translation changed: %+v", display)
			}
		})
	}
}

func TestPluginsDoNotBlockOwnerAndCancelOnShutdown(t *testing.T) {
	for _, outgoing := range []bool{false, true} {
		t.Run(fmt.Sprint(outgoing), func(t *testing.T) {
			dir := t.TempDir()
			ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
			plugin := testPlugin(t, "gate")
			plugin.Command = append(plugin.Command, ready, release)
			plugin.Timeout = 10 * time.Second
			actions := make(chan UserAction, 8)
			events := make(chan DisplayEvent, 1024)
			c := newClient(&config{path: filepath.Join(dir, "config.json"), Views: []View{{Name: "lobby"}}, ActiveViewName: "lobby", AnchorRelays: []string{"ws://127.0.0.1:1"}}, actions, events)
			c.plugins = messagePlugins{}
			if outgoing {
				c.plugins.OnSend = plugin
				c.relays["inert"] = &managedRelay{url: "inert", connected: true, chats: []string{"lobby"}}
				c.publishMessage("original")
				delete(c.relays, "inert")
			} else {
				c.plugins.OnReceive = plugin
				c.receiveJobs = make(chan receivePluginJob, 32)
				c.receiveResults = make(chan receivePluginResult, 32)
				c.processEvent(signedEvent(t, "original"), "local")
			}
			done := make(chan struct{})
			go func() { defer close(done); c.Run() }()
			defer func() { c.Stop(); <-done }()
			if !waitUntil(t, 3*time.Second, func() bool { _, err := os.Stat(ready); return err == nil }) {
				t.Fatal("plugin did not start")
			}
			actions <- UserAction{Type: "RESOLVE_MESSAGE_TARGET", Payload: "probe"}
			deadline := time.After(time.Second)
			for {
				select {
				case e := <-events:
					if e.Type != "MESSAGE_TARGET" {
						continue
					}
					c.Stop()
					select {
					case <-done:
						return
					case <-time.After(time.Second):
						t.Fatal("shutdown did not cancel running plugin")
					}
				case <-deadline:
					t.Fatal("plugin blocked owner actions")
				}
			}
		})
	}
}

func TestIncomingPluginQueueAndClearBoundary(t *testing.T) {
	c, events := testClient(t)
	c.plugins = messagePlugins{OnReceive: testPlugin(t, "replace")}
	c.receiveJobs = make(chan receivePluginJob, 2)
	for i := range 5 {
		c.processEvent(signedEvent(t, fmt.Sprint(i)), "local")
	}
	if len(c.receiveJobs) != 2 || len(events) != 1 || (<-events).Type != "WARN" || c.userContext.Len() != 0 {
		t.Fatal("queue overload was not bounded, suppressed or reported once")
	}
	inFlight := <-c.receiveJobs
	c.handleAction(UserAction{Type: "CLEAR"})
	if (<-events).Type != "CLEARED" {
		t.Fatal("missing clear boundary")
	}
	if len(c.receiveJobs) != 0 {
		t.Fatal("clear retained obsolete queued plugin work")
	}
	c.finishReceivePlugin(receivePluginResult{job: inFlight, content: "stale"})
	if len(events) != 0 || c.userContext.Len() != 0 {
		t.Fatal("pending presentation returned after clear")
	}
	c.processEvent(signedEvent(t, "fresh visible"), "local")
	job := <-c.receiveJobs
	c.finishReceivePlugin(receivePluginResult{job: job, content: "visible"})
	if len(events) != 1 || (<-events).Content != "visible" || c.userContext.Len() != 1 {
		t.Fatal("new presentations did not resume after clear")
	}
	c.processEvent(signedEvent(t, "pending before view switch"), "local")
	job = <-c.receiveJobs
	c.config.Views = append(c.config.Views, View{Name: "other"})
	c.config.ActiveViewName = "other"
	c.userContext.Purge()
	c.finishReceivePlugin(receivePluginResult{job: job, content: "stale view"})
	if len(events) != 0 || c.userContext.Len() != 0 {
		t.Fatal("pending presentation survived switching away from its chat")
	}
}

func TestExecMessagePluginDeadlineAndCancellation(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelParent), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			plugin := testPlugin(t, "wait")
			plugin.Timeout = 30 * time.Millisecond
			if cancelParent {
				plugin.Timeout = 3 * time.Second
				time.AfterFunc(30*time.Millisecond, cancel)
			}
			start := time.Now()
			_, _, err := runMessagePlugin(ctx, plugin, pluginRequest{Version: 1})
			if err == nil || time.Since(start) > time.Second {
				t.Fatalf("execution not bounded/canceled: elapsed=%v err=%v", time.Since(start), err)
			}
		})
	}
}

func testGatedPlugin(t *testing.T, mode string) (*pluginConfig, string, string) {
	t.Helper()
	dir := t.TempDir()
	ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	plugin := testPlugin(t, "gate")
	plugin.Command = append(plugin.Command, ready, release, mode)
	plugin.Timeout = 10 * time.Second
	return plugin, ready, release
}

func TestIncomingPluginOrderAcrossViewSwitch(t *testing.T) {
	plugin, ready, release := testGatedPlugin(t, "unchanged")
	cfg := relayTestConfig(t, "ws://127.0.0.1:1")
	cfg.Views = append(cfg.Views, View{Name: "other"})
	h := startRelayHarness(t, cfg, func(c *client) { c.plugins.OnReceive = plugin })
	worker := &managedRelay{url: "ws://127.0.0.1:1"}
	h.inject(relayEvent{worker: worker, event: signedEvent(t, "obsolete lobby message")})
	if !waitUntil(t, 5*time.Second, func() bool { _, err := os.Stat(ready); return err == nil }) {
		t.Fatal("plugin did not start")
	}
	h.send(UserAction{Type: "ACTIVATE_VIEW", Payload: "other"})
	h.waitForEvent("other view active while plugin is running", func(e DisplayEvent) bool {
		state, ok := e.Payload.(StateUpdate)
		return e.Type == "STATE_UPDATE" && ok && state.Views[state.ActiveViewIndex].Name == "other"
	})
	for _, content := range []string{"first", "second", "barrier"} {
		event := signedChatEvent(t, transportTestKey, ephChatKind, "other", content, nostr.Now())
		h.inject(relayEvent{worker: worker, event: event})
		h.inject(relayEvent{worker: worker, event: event})
	}
	writeTestFile(t, release, "release", 0600)
	h.waitMessage("barrier") // FIFO barrier through admission, execution and display.
	h.stop()
	var messages []string
	for _, e := range h.snapshot() {
		if e.Type == "NEW_MESSAGE" {
			if e.Chat != "other" {
				t.Fatalf("pending plugin result leaked into the wrong chat: %+v", e)
			}
			messages = append(messages, e.Content)
		}
	}
	if !reflect.DeepEqual(messages, []string{"first", "second", "barrier"}) {
		t.Fatalf("messages were lost, duplicated or reordered: %q", messages)
	}
}

func TestIncomingPluginModerationBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name, mode, filter, mute string
		action                   UserAction
		admitted                 bool
		visible                  bool
	}{
		{name: "filter_rejects_original", mode: "replace", filter: "translated"},
		{name: "replacement_leaves_filter", mode: "replace", filter: "original", admitted: true, visible: true},
		{name: "replacement_enters_mute", mode: "replace", mute: "translated", admitted: true, visible: true},
		{name: "mute_rejects_original", mode: "replace", mute: "original"},
		{name: "mute_overrides_filter", mode: "replace", filter: "original", mute: "original"},
		{name: "enable_filter_in_flight", mode: "replace", action: UserAction{Type: "HANDLE_FILTER", Payload: "translated"}, admitted: true, visible: true},
		{name: "remove_filter_in_flight", mode: "replace", filter: "original", action: UserAction{Type: "CLEAR_FILTERS"}, admitted: true, visible: true},
		{name: "enable_mute_in_flight", mode: "replace", action: UserAction{Type: "HANDLE_MUTE", Payload: "original"}, admitted: true, visible: true},
		{name: "remove_mute_in_flight", mode: "replace", mute: "translated", action: UserAction{Type: "CLEAR_MUTES"}, admitted: true, visible: true},
		{name: "failure_preserves_admitted_original", mode: "fail", action: UserAction{Type: "HANDLE_FILTER", Payload: "translated"}, admitted: true, visible: true},
		{name: "malformed_preserves_admitted_original", mode: "malformed", action: UserAction{Type: "HANDLE_MUTE", Payload: "original"}, admitted: true, visible: true},
		{name: "timeout_preserves_admitted_original", mode: "timeout", action: UserAction{Type: "HANDLE_MUTE", Payload: "original"}, admitted: true, visible: true},
		{name: "suppression", mode: "suppress", filter: "original", admitted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, events := testClient(t)
			c.config.path = filepath.Join(t.TempDir(), "config.json")
			if tc.filter != "" {
				c.config.Filters = []filter{{Pattern: tc.filter, Enabled: true}}
			}
			if tc.mute != "" {
				c.config.Mutes = []filter{{Pattern: tc.mute, Enabled: true}}
			}
			c.rebuildRegexCaches()
			plugin, ready, release := testGatedPlugin(t, tc.mode)
			if tc.mode == "timeout" {
				plugin.Timeout = 2 * time.Second
			}
			c.plugins.OnReceive = plugin
			c.receiveJobs = make(chan receivePluginJob, 32)
			c.receiveResults = make(chan receivePluginResult, 32)
			defer c.shutdown()
			original := signedEvent(t, "original")
			c.processEvent(original, "local")
			c.processEvent(original, "duplicate")
			if !tc.admitted {
				if len(c.receiveJobs) != 0 || len(events) != 0 || c.userContext.Len() != 0 {
					t.Fatal("rejected original reached the plugin queue or display")
				}
				return
			}
			if len(c.receiveJobs) != 1 {
				t.Fatal("admitted message was not queued exactly once")
			}
			c.wg.Go(c.receivePluginLoop)
			if !waitUntil(t, 5*time.Second, func() bool { _, err := os.Stat(ready); return err == nil }) {
				t.Fatal("validated message did not reach the running plugin")
			}
			// The owner changes rules only after the subprocess is actually running.
			if tc.action.Type != "" {
				c.handleAction(tc.action)
			}
			for len(events) > 0 {
				<-events
			}
			if tc.mode != "timeout" {
				writeTestFile(t, release, "release", 0600)
			}
			select {
			case result := <-c.receiveResults:
				wantError := tc.mode == "fail" || tc.mode == "malformed" || tc.mode == "timeout"
				if (result.err != nil) != wantError {
					t.Fatalf("unexpected plugin outcome: %v", result.err)
				}
				c.finishReceivePlugin(result)
			case <-time.After(5 * time.Second):
				t.Fatal("plugin did not finish")
			}
			var messages []string
			for len(events) > 0 {
				if e := <-events; e.Type == "NEW_MESSAGE" {
					messages = append(messages, e.Content)
				}
			}
			if tc.visible {
				want := "translated"
				if tc.mode == "fail" || tc.mode == "malformed" || tc.mode == "timeout" {
					want = "original"
				}
				if !reflect.DeepEqual(messages, []string{want}) || c.userContext.Len() != 1 {
					t.Fatalf("expected admitted message %q, got %q, participants=%d", want, messages, c.userContext.Len())
				}
			} else if len(messages) != 0 || c.userContext.Len() != 0 {
				t.Fatalf("moderated message leaked: %q, participants=%d", messages, c.userContext.Len())
			}
			if len(c.receiveJobs) != 0 || len(c.receiveResults) != 0 {
				t.Fatal("duplicate was processed twice")
			}
		})
	}
}

func TestPluginClearDuringExecution(t *testing.T) {
	for _, mode := range []string{"replace", "fail"} {
		t.Run(mode, func(t *testing.T) {
			c, events := testClient(t)
			c.ensureViewIdentities(c.getActiveView())
			identity := c.chatKeys["lobby"]
			worker := &managedRelay{url: "ws://127.0.0.1:1"}
			c.relays[worker.url] = worker
			plugin, ready, release := testGatedPlugin(t, mode)
			c.plugins.OnReceive = plugin
			c.receiveJobs = make(chan receivePluginJob, 32)
			c.receiveResults = make(chan receivePluginResult, 32)
			c.wg.Go(c.receivePluginLoop)
			defer c.shutdown()
			old, queued := signedEvent(t, "old"), signedEvent(t, "queued")
			c.processEvent(old, worker.url)
			if !waitUntil(t, 5*time.Second, func() bool { _, err := os.Stat(ready); return err == nil }) {
				t.Fatal("plugin did not start")
			}
			c.processEvent(queued, worker.url)
			for range 2 {
				c.handleAction(UserAction{Type: "CLEAR"})
				if e := <-events; e.Type != "CLEARED" {
					t.Fatalf("unexpected clear output: %+v", e)
				}
			}
			if c.userContext.Len() != 0 || c.authorVerify.len() != 0 || len(c.receiveJobs) != 0 {
				t.Fatal("clear retained participants or queued work")
			}
			if c.chatKeys["lobby"] != identity || c.relays[worker.url] != worker {
				t.Fatal("clear changed the identity or relay handle")
			}
			c.processEvent(old, worker.url)
			c.processEvent(queued, worker.url)
			if len(c.receiveJobs) != 0 || !c.seenCache.Contains(old.ID) || !c.seenCache.Contains(queued.ID) {
				t.Fatal("clear lost replay protection")
			}
			// New work can queue before the obsolete process exits; it must survive.
			c.plugins.OnReceive = testPlugin(t, "unchanged")
			c.processEvent(signedEvent(t, "fresh"), worker.url)
			writeTestFile(t, release, "release", 0600)
			for i, want := range []string{"old", "fresh"} {
				select {
				case result := <-c.receiveResults:
					if result.job.request.Content != want || (i == 1 && result.err != nil) {
						t.Fatalf("unexpected result after clear: %+v", result)
					}
					c.finishReceivePlugin(result)
				case <-time.After(5 * time.Second):
					t.Fatal("new traffic did not resume after clear")
				}
				if i == 0 && (len(events) != 0 || c.userContext.Len() != 0) {
					t.Fatal("obsolete result restored cleared state or emitted a warning")
				}
			}
			if len(events) != 1 || (<-events).Content != "fresh" || c.userContext.Len() != 1 {
				t.Fatal("clear dropped new traffic or restored old traffic")
			}
		})
	}
	t.Run("outgoing_sends_are_preserved", func(t *testing.T) {
		c, events := groupDestinationClient(t, 0)
		plugin, ready, release := testGatedPlugin(t, "unchanged")
		c.plugins.OnSend = plugin
		worker := c.relays["named"]
		worker.publish = make(chan relayPublish, 1)
		identity := c.chatKeys["lobby"]
		c.publishMessageInView("running", "group", "lobby", "")
		c.wg.Go(c.publishLoop)
		defer c.shutdown()
		if !waitUntil(t, 5*time.Second, func() bool { _, err := os.Stat(ready); return err == nil }) {
			t.Fatal("outgoing plugin did not start")
		}
		c.publishMessageInView("queued", "group", "lobby", "")
		c.handleAction(UserAction{Type: "CLEAR"})
		if e := <-events; e.Type != "CLEARED" || len(c.outgoing) != 1 {
			t.Fatalf("clear discarded an accepted send or lost its boundary: %+v", e)
		}
		writeTestFile(t, release, "release", 0600)
		for _, want := range []string{"running", "queued"} {
			select {
			case request := <-worker.publish:
				if request.event.Content != want || request.event.PubKey.Hex() != identity.pubKey || request.event.Tags.Find("d")[1] != "lobby" {
					t.Fatalf("clear changed a pending publication: %+v", request.event)
				}
				request.result <- nil
			case <-time.After(5 * time.Second):
				t.Fatal("clear canceled a pending send")
			}
			select {
			case result := <-c.results:
				if result.accepted != 1 {
					t.Fatalf("send did not complete after clear: %+v", result)
				}
				// Publication after clear is new traffic, not a stale presentation.
				c.processEvent(&result.event, "local")
			case <-time.After(5 * time.Second):
				t.Fatal("publication result did not arrive")
			}
			if len(events) != 1 {
				t.Fatalf("expected exactly one new echo after clear, got %d", len(events))
			}
			if e := <-events; e.Type != "NEW_MESSAGE" || e.Content != want || !e.IsOwnMessage {
				t.Fatalf("new outgoing echo lost after clear: %+v", e)
			}
		}
	})
}
