package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Timeout: 5 * time.Second, Transport: transport}
}

func TestRunTranslatesMessages(t *testing.T) {
	for _, tt := range []struct {
		plugin, content, language, translation, sendLanguage, receiveLanguage string
	}{
		{"on_send", "Привет\nмир", "French", "Bonjour\nle monde", "", ""},
		{"on_receive", "Bonjour\nle monde", "Russian", "Привет\nмир", "", ""},
		{"on_send", "Привет\nмир", "Italian", "Ciao\nmondo", "Italian", "German"},
		{"on_receive", "Bonjour\nle monde", "German", "Hallo\nWelt", "Italian", "German"},
	} {
		t.Run(tt.plugin+"_"+tt.language, func(t *testing.T) {
			var oauthCalls, chatCalls atomic.Int32
			t.Setenv("TRANSLATE_PLUGIN_API_KEY", "  test-key  ")
			t.Setenv("TRANSLATE_PLUGIN_ACCESS_TOKEN", "")
			t.Setenv("TRANSLATE_PLUGIN_TARGET_LANGUAGE", tt.sendLanguage)
			t.Setenv("TRANSLATE_PLUGIN_RECEIVE_LANGUAGE", tt.receiveLanguage)

			auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				oauthCalls.Add(1)
				if r.URL.Path != "/oauth" {
					t.Errorf("OAuth path = %q", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Basic test-key" {
					t.Errorf("OAuth Authorization = %q", got)
				}
				if r.Header.Get("RqUID") == "" {
					t.Error("OAuth RqUID is empty")
				}
				if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
					t.Errorf("OAuth Content-Type = %q", got)
				}
				body, _ := io.ReadAll(r.Body)
				if got := string(body); got != "scope=GIGACHAT_API_PERS" {
					t.Errorf("OAuth body = %q", got)
				}
				io.WriteString(w, `{"access_token":"test-token"}`)
			}))
			defer auth.Close()

			chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				chatCalls.Add(1)
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("chat path = %q", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("chat Authorization = %q", got)
				}
				var sent chatRequest
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					t.Errorf("decode chat request: %v", err)
					return
				}
				if sent.Model != "GigaChat-2" {
					t.Errorf("chat model = %q", sent.Model)
				}
				if len(sent.Messages) != 2 || sent.Messages[0].Role != "system" || !strings.HasPrefix(sent.Messages[0].Content, "Translate the user's public chat message into "+tt.language+".") || sent.Messages[1].Role != "user" || sent.Messages[1].Content != tt.content {
					t.Errorf("chat request did not select %s or preserve the original content", tt.language)
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": tt.translation}}}})
			}))
			defer chat.Close()

			cfg := configFromEnv()
			cfg.base, cfg.auth = chat.URL+"/v1", auth.URL+"/oauth"
			cfg.model, cfg.scope = "GigaChat-2", "GIGACHAT_API_PERS"
			cfg.client = testClient()
			req, err := json.Marshal(map[string]any{"version": 1, "plugin": tt.plugin, "content": tt.content, "sender": map[string]string{"pubkey": "a", "nick": "b"}, "future_field": true})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := run(context.Background(), bytes.NewReader(append(req, '\n')), &out, cfg); err != nil {
				t.Fatalf("run: %v", err)
			}
			text, _ := json.Marshal(tt.translation)
			want := `{"version":1,"action":"replace","content":` + string(text) + "}\n"
			if got := out.String(); got != want {
				t.Fatalf("response = %q, want %q", got, want)
			}
			if strings.Count(out.String(), "\n") != 1 {
				t.Fatal("response is not a single line")
			}
			if oauthCalls.Load() != 1 || chatCalls.Load() != 1 {
				t.Fatalf("OAuth calls = %d, chat calls = %d", oauthCalls.Load(), chatCalls.Load())
			}
		})
	}
}

func TestRunLeavesUnsupportedOrBlankUnchanged(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer srv.Close()

	cfg := config{base: srv.URL, auth: srv.URL, client: testClient()}
	for _, req := range []string{
		`{"version":1,"plugin":"on_edit","content":"hello"}`,
		`{"version":1,"plugin":"on_send","content":"  "}`,
		`{"version":1,"plugin":"on_receive","content":"  "}`,
	} {
		var out bytes.Buffer
		if err := run(context.Background(), strings.NewReader(req+"\n"), &out, cfg); err != nil {
			t.Fatalf("run(%s): %v", req, err)
		}
		want := `{"version":1,"action":"unchanged"}` + "\n"
		if got := out.String(); got != want {
			t.Fatalf("run(%s) = %q, want %q", req, got, want)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("HTTP calls = %d, want 0", calls.Load())
	}
}

func TestRunUsesConfiguredAccessToken(t *testing.T) {
	t.Setenv("TRANSLATE_PLUGIN_ACCESS_TOKEN", "direct-token")
	t.Setenv("TRANSLATE_PLUGIN_API_KEY", "unused-key")
	var authCalls atomic.Int32
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCalls.Add(1)
		http.Error(w, "OAuth must not be called", http.StatusInternalServerError)
	}))
	defer auth.Close()

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer direct-token" {
			t.Errorf("chat Authorization = %q", got)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"translated"}}]}`)
	}))
	defer chat.Close()

	cfg := configFromEnv()
	cfg.base, cfg.auth = chat.URL+"/v1", auth.URL
	cfg.client = testClient()
	req := `{"version":1,"plugin":"on_send","content":"hello"}`

	var out bytes.Buffer
	if err := run(context.Background(), strings.NewReader(req+"\n"), &out, cfg); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := `{"version":1,"action":"replace","content":"translated"}` + "\n"
	if got := out.String(); got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
	if authCalls.Load() != 0 {
		t.Fatalf("OAuth calls = %d, want 0", authCalls.Load())
	}
}

func TestRunFailuresKeepStdoutEmptyAndHideBodies(t *testing.T) {
	const secret = "sensitive-api-response-body"
	req := `{"version":1,"plugin":"on_send","content":"hello"}`

	t.Run("missing or invalid credentials", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
		}))
		defer srv.Close()
		for _, key := range []string{"", "  	", "private-key-marker\ninvalid"} {
			var out bytes.Buffer
			err := run(context.Background(), strings.NewReader(req+"\n"), &out, config{key: key, base: srv.URL, auth: srv.URL, client: testClient()})
			if err == nil || !strings.Contains(err.Error(), "TRANSLATE_PLUGIN_API_KEY") || strings.Contains(err.Error(), "private-key-marker") || out.Len() != 0 || calls.Load() != 0 {
				t.Fatalf("invalid credentials must fail safely before HTTP: err=%v stdout_bytes=%d calls=%d", err, out.Len(), calls.Load())
			}
		}
	})

	t.Run("OAuth failure", func(t *testing.T) {
		auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, secret)
		}))
		defer auth.Close()

		cfg := config{
			base:   "http://127.0.0.1:1/v1",
			auth:   auth.URL,
			model:  "GigaChat-2",
			key:    "test-key",
			target: "English",
			client: testClient(),
		}
		var out bytes.Buffer
		err := run(context.Background(), strings.NewReader(req+"\n"), &out, cfg)
		if err == nil {
			t.Fatal("expected an error")
		}
		if out.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", out.String())
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked response body: %v", err)
		}
	})

	t.Run("chat failure", func(t *testing.T) {
		auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"access_token":"test-token"}`)
		}))
		defer auth.Close()

		chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, secret)
		}))
		defer chat.Close()

		cfg := config{
			base:   chat.URL + "/v1",
			auth:   auth.URL,
			model:  "GigaChat-2",
			key:    "test-key",
			target: "English",
			client: testClient(),
		}
		var out bytes.Buffer
		err := run(context.Background(), strings.NewReader(req+"\n"), &out, cfg)
		if err == nil {
			t.Fatal("expected an error")
		}
		if out.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", out.String())
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked response body: %v", err)
		}
		if strings.Contains(err.Error(), "test-token") {
			t.Fatalf("error leaked access token: %v", err)
		}
	})
}

func TestRunRejectsInvalidRequests(t *testing.T) {
	cfg := config{client: testClient()}
	for _, req := range []string{
		`{"version":2,"plugin":"on_send","content":"hi"}`,
		`{"version":1,`,
	} {
		var out bytes.Buffer
		if err := run(context.Background(), strings.NewReader(req+"\n"), &out, cfg); err == nil {
			t.Fatalf("run(%s) unexpectedly succeeded", req)
		}
		if out.Len() != 0 {
			t.Fatalf("run(%s) stdout = %q, want empty", req, out.String())
		}
	}
}
