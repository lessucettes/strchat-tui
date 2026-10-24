package client

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scrollbarEvents drains every already-queued display event.
func scrollbarEvents(events chan DisplayEvent) []DisplayEvent {
	var out []DisplayEvent
	for {
		select {
		case ev := <-events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestSetSameScrollbarShowsHintWithoutSaving(t *testing.T) {
	for _, mode := range []ScrollbarPosition{ScrollbarOff, ScrollbarRight, ScrollbarLeft} {
		t.Run(mode.String(), func(t *testing.T) {
			c, events := testClient(t)
			c.config.Scrollbar = mode
			// Re-selecting the current mode must not attempt to save.
			c.config.path = ""

			c.setScrollbar(strconv.Itoa(int(mode)))

			evs := scrollbarEvents(events)
			if len(evs) != 1 || evs[0].Type != "INFO" {
				t.Fatalf("query events = %+v, want one INFO", evs)
			}
			for _, want := range []string{"Scrollbar: " + mode.String(), "0 off", "1 right", "2 left", "/scrollbar [0|1|2]", "/sb"} {
				if !strings.Contains(evs[0].Content, want) {
					t.Errorf("query output %q is missing %q", evs[0].Content, want)
				}
			}
			if c.config.Scrollbar != mode {
				t.Errorf("query changed scrollbar to %v, want %v", c.config.Scrollbar, mode)
			}
		})
	}
}

func TestSetScrollbarCyclesSilentlyAndPersists(t *testing.T) {
	c, events := testClient(t)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	c.config.Scrollbar = ScrollbarOff
	for _, want := range []ScrollbarPosition{ScrollbarRight, ScrollbarLeft, ScrollbarOff} {
		c.handleAction(UserAction{Type: "SET_SCROLLBAR", Payload: "   "})
		evs := scrollbarEvents(events)
		if len(evs) != 1 || evs[0].Type != "SCROLLBAR_UPDATE" || evs[0].Payload != want {
			t.Fatalf("cycle events = %+v, want only SCROLLBAR_UPDATE %v", evs, want)
		}
		reloaded, err := loadConfigFrom(c.config.path)
		if err != nil || reloaded.Scrollbar != want || c.config.Scrollbar != want {
			t.Fatalf("cycle to %v did not persist (load error: %v)", want, err)
		}
	}
}

func TestSetScrollbarPersistsAcrossReload(t *testing.T) {
	cases := []struct {
		arg  string
		want ScrollbarPosition
	}{
		{"0", ScrollbarOff},
		{" 1 ", ScrollbarRight},
		{"2", ScrollbarLeft},
	}
	for _, tc := range cases {
		t.Run(tc.want.String(), func(t *testing.T) {
			c, events := testClient(t)
			c.config.path = filepath.Join(t.TempDir(), "config.json")
			c.config.Nick = "alice"
			c.config.Theme = DefaultTheme
			c.config.Scrollbar = ScrollbarRight
			if tc.want == ScrollbarRight {
				c.config.Scrollbar = ScrollbarOff
			}
			if err := c.config.save(); err != nil {
				t.Fatal(err)
			}

			c.handleAction(UserAction{Type: "SET_SCROLLBAR", Payload: tc.arg})

			if c.config.Scrollbar != tc.want {
				t.Fatalf("live scrollbar = %v, want %v", c.config.Scrollbar, tc.want)
			}
			evs := scrollbarEvents(events)
			var sawUpdate, sawInfo bool
			for _, ev := range evs {
				switch ev.Type {
				case "ERROR":
					t.Fatalf("successful selection reported an error: %s", ev.Content)
				case "SCROLLBAR_UPDATE":
					sawUpdate = true
					got, ok := ev.Payload.(ScrollbarPosition)
					if !ok || got != tc.want {
						t.Fatalf("SCROLLBAR_UPDATE payload = %#v, want %v", ev.Payload, tc.want)
					}
				case "INFO":
					sawInfo = sawInfo || strings.Contains(ev.Content, "Scrollbar: "+tc.want.String())
				}
			}
			if !sawUpdate || !sawInfo {
				t.Fatalf("successful selection events = %+v, want SCROLLBAR_UPDATE and INFO", evs)
			}

			reloaded, err := loadConfigFrom(c.config.path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reloaded, c.config) {
				t.Fatal("scrollbar save/load changed other settings or lost the position")
			}

			raw, err := os.ReadFile(c.config.path)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if tc.want != ScrollbarOff {
				var stored ScrollbarPosition
				if err := json.Unmarshal(fields["scrollbar"], &stored); err != nil || stored != tc.want {
					t.Fatalf("stored scrollbar = %q, want %v (decode error: %v)", fields["scrollbar"], tc.want, err)
				}
			}
		})
	}
}

func TestSaveRejectsInvalidScrollbar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	conf := &config{Scrollbar: ScrollbarLeft + 1, path: path}

	err := conf.save()
	if err == nil {
		t.Fatal("expected save to reject an out-of-range scrollbar position")
	}
	if !strings.Contains(err.Error(), "scrollbar") {
		t.Errorf("error %q does not mention the scrollbar field", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("config file exists after a rejected save (stat err: %v)", statErr)
	}
}

func TestLoadScrollbarConfig(t *testing.T) {
	valid := []struct {
		name string
		data string
		want ScrollbarPosition
	}{
		{"missing_right_default", `{"nick":"alice"}`, ScrollbarRight},
		{"explicit_off", `{"scrollbar":0}`, ScrollbarOff},
		{"right", `{"scrollbar":1}`, ScrollbarRight},
		{"left", `{"scrollbar":2}`, ScrollbarLeft},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			writeTestFile(t, path, tc.data, 0o600)

			conf, err := loadConfigFrom(path)
			if err != nil {
				t.Fatalf("loadConfigFrom: %v", err)
			}
			if conf.Scrollbar != tc.want {
				t.Fatalf("loaded scrollbar = %v, want %v", conf.Scrollbar, tc.want)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != tc.data {
				t.Fatal("loading a scrollbar setting rewrote the config")
			}
		})
	}

	invalid := []struct {
		name       string
		data       string
		wantPhrase string
	}{
		{"above_range", `{"scrollbar":3}`, "scrollbar"},
		{"negative", `{"scrollbar":-1}`, "scrollbar"},
		{"wrong_type", `{"scrollbar":"1","nick":"SENSITIVE-MARKER"}`, "decode"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			writeTestFile(t, path, tc.data, 0o600)

			_, err := loadConfigFrom(path)
			if err == nil {
				t.Fatal("expected an error for an invalid scrollbar setting")
			}
			if !strings.Contains(err.Error(), tc.wantPhrase) {
				t.Errorf("error %q does not mention %q", err, tc.wantPhrase)
			}
			requireNoContentEcho(t, err, "SENSITIVE-MARKER")
		})
	}
}

func TestSetScrollbarRejectsInvalidInputWithoutMutation(t *testing.T) {
	c, events := testClient(t)
	path := filepath.Join(t.TempDir(), "config.json")
	c.config.path, c.config.Scrollbar = path, ScrollbarRight
	if err := c.config.save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, arg := range []string{"3", "-1", "left", "1 2", "01", "+1"} {
		c.handleAction(UserAction{Type: "SET_SCROLLBAR", Payload: arg})
		if c.config.Scrollbar != ScrollbarRight {
			t.Fatalf("argument %q changed live scrollbar to %v", arg, c.config.Scrollbar)
		}
		evs := scrollbarEvents(events)
		if len(evs) != 1 || evs[0].Type != "ERROR" || !strings.Contains(evs[0].Content, "0 (off), 1 (right), or 2 (left)") {
			t.Fatalf("argument %q events = %+v, want one range ERROR", arg, evs)
		}
	}

	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected scrollbar input changed the saved config")
	}
}

func TestSetScrollbarSaveFailureKeepsLiveAndSavedState(t *testing.T) {
	c, events := testClient(t)
	path := filepath.Join(t.TempDir(), "config.json")
	c.config.path, c.config.Scrollbar = path, ScrollbarLeft
	if err := c.config.save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Replacing a non-empty directory must fail, even when tests run as root.
	c.config.path = t.TempDir()
	c.handleAction(UserAction{Type: "SET_SCROLLBAR", Payload: "1"})

	if c.config.Scrollbar != ScrollbarLeft {
		t.Fatal("failed save changed the live scrollbar position")
	}
	evs := scrollbarEvents(events)
	if len(evs) != 1 || evs[0].Type != "ERROR" || !strings.Contains(evs[0].Content, "Failed to save scrollbar") {
		t.Fatalf("failed save events = %+v, want one visible save error", evs)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed save changed the previously saved config")
	}
}

func TestHelpPlacesScrollbarAfterTheme(t *testing.T) {
	c, events := testClient(t)
	c.getHelp("appearance")
	ev := <-events
	theme := strings.Index(ev.Content, "/theme ")
	scrollbar := strings.Index(ev.Content, "/scrollbar ")
	if theme < 0 || scrollbar < 0 {
		t.Fatalf("help is missing theme or scrollbar: %q", ev.Content)
	}
	if theme >= scrollbar {
		t.Fatalf("scrollbar help is not immediately after theme: %q", ev.Content)
	}
	if !strings.Contains(ev.Content, "(Alias: /sb)") {
		t.Fatal("scrollbar help does not document the /sb alias")
	}
}

func TestRunEmitsScrollbarUpdateAfterTheme(t *testing.T) {
	events := make(chan DisplayEvent, 256)
	actions := make(chan UserAction)
	c := newClient(&config{AnchorRelays: []string{"ws://127.0.0.1:1"}, Scrollbar: ScrollbarLeft}, actions, events)
	done := make(chan struct{})
	go func() {
		c.Run()
		close(done)
	}()
	defer func() {
		c.Stop()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not stop")
		}
	}()

	deadline := time.After(10 * time.Second)
	sawTheme, sawScrollbar := false, false
	for !sawScrollbar {
		select {
		case ev := <-events:
			switch ev.Type {
			case "THEME_UPDATE":
				sawTheme = true
			case "SCROLLBAR_UPDATE":
				sawScrollbar = true
				if !sawTheme {
					t.Fatal("SCROLLBAR_UPDATE was emitted before THEME_UPDATE")
				}
				got, ok := ev.Payload.(ScrollbarPosition)
				if !ok || got != ScrollbarLeft {
					t.Fatalf("startup SCROLLBAR_UPDATE payload = %#v, want %v", ev.Payload, ScrollbarLeft)
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for startup SCROLLBAR_UPDATE")
		}
	}
	if !sawTheme {
		t.Fatal("startup THEME_UPDATE was not emitted first")
	}
}
