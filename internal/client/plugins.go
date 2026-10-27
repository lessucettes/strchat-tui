package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const maxPluginOutputBytes = 128 << 10

// Only exec is supported. The same versioned JSON objects can later be framed
// as JSON Lines on a persistent process without changing message semantics.
type pluginConfig struct {
	Command []string
	Timeout time.Duration
}

type messagePlugins struct {
	OnSend    *pluginConfig
	OnReceive *pluginConfig
}

func newMessagePlugins(options Options) (messagePlugins, error) {
	var plugins messagePlugins
	timeout := options.PluginTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	if timeout < 0 || timeout > 2*time.Minute {
		return plugins, errors.New("plugin timeout must be positive and at most 2m")
	}
	for _, entry := range []struct {
		name, path string
		target     **pluginConfig
	}{
		{"plugin-send", options.PluginSend, &plugins.OnSend},
		{"plugin-receive", options.PluginReceive, &plugins.OnReceive},
	} {
		if entry.path == "" {
			continue
		}
		if strings.TrimSpace(entry.path) == "" {
			return plugins, fmt.Errorf("%s executable cannot be blank", entry.name)
		}
		path, err := exec.LookPath(entry.path)
		if err != nil {
			return plugins, fmt.Errorf("%s executable is unavailable: %w", entry.name, err)
		}
		*entry.target = &pluginConfig{Command: []string{path}, Timeout: timeout}
	}
	return plugins, nil
}

func (h *pluginConfig) validate() error {
	if h == nil {
		return nil
	}

	if len(h.Command) == 0 || h.Command[0] == "" {
		return errors.New("command must contain an executable and optional arguments")
	}
	if h.Timeout < 0 || h.Timeout > 2*time.Minute {
		return errors.New("plugin timeout must be positive and at most 2m")
	}
	for _, arg := range h.Command {
		if bytes.IndexByte([]byte(arg), 0) >= 0 {
			return errors.New("command arguments must not contain NUL")
		}
	}
	return nil
}

type pluginSender struct {
	PubKey string `json:"pubkey"`
	Nick   string `json:"nick"`
}

// This explicit allowlist deliberately excludes session keys and mutable state.
type pluginRequest struct {
	Version   int          `json:"version"`
	Plugin    string       `json:"plugin"`
	Content   string       `json:"content"`
	Sender    pluginSender `json:"sender"`
	EventID   string       `json:"event_id,omitempty"`
	CreatedAt int64        `json:"created_at,omitempty"`
}

type pluginResponse struct {
	Version int     `json:"version"`
	Action  string  `json:"action"`
	Content *string `json:"content,omitempty"`
}

// A writer cap, rather than truncating after Output(), bounds allocations even
// for a broken plugin. Stderr is bounded too and is never echoed into the TUI.
type pluginOutput struct {
	data     bytes.Buffer
	overflow bool
}

func (b *pluginOutput) Write(p []byte) (int, error) {
	if len(p) > maxPluginOutputBytes-b.data.Len() {
		b.overflow = true
		return 0, errors.New("plugin output exceeds 128 KiB")
	}
	return b.data.Write(p)
}

func runMessagePlugin(parent context.Context, h *pluginConfig, request pluginRequest) (content string, suppress bool, err error) {
	if h == nil {
		return request.Content, false, nil
	}
	if err := h.validate(); err != nil {
		return "", false, err
	}
	timeout := h.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	input, err := json.Marshal(request)
	if err != nil {
		return "", false, fmt.Errorf("encode plugin request: %w", err)
	}
	cmd := exec.CommandContext(ctx, h.Command[0], h.Command[1:]...)
	cmd.Stdin = bytes.NewReader(append(input, '\n'))
	// Bound Wait even if a misbehaving descendant retains the stdio pipes.
	cmd.WaitDelay = 100 * time.Millisecond
	var stdout, stderr pluginOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", false, fmt.Errorf("plugin execution: %w", ctx.Err())
		}
		// Do not expose executable arguments, stderr or external API responses.
		return "", false, errors.New("plugin process failed to start, exited unsuccessfully or exceeded its output limit")
	}
	if stdout.overflow || stderr.overflow {
		return "", false, errors.New("plugin output exceeds 128 KiB")
	}
	var response pluginResponse
	if !utf8.Valid(stdout.data.Bytes()) || json.Unmarshal(stdout.data.Bytes(), &response) != nil || response.Version != 1 {
		return "", false, errors.New("invalid plugin response: expected one version 1 JSON object")
	}
	switch response.Action {
	case "unchanged", "suppress":
		if response.Content != nil {
			return "", false, errors.New("invalid plugin response: content requires replace action")
		}
		return request.Content, response.Action == "suppress", nil
	case "replace":
		if response.Content != nil {
			return *response.Content, false, nil
		}
	}
	return "", false, errors.New("invalid plugin response: expected unchanged, suppress or replace with content")
}

type receivePluginJob struct {
	plugin  *pluginConfig
	request pluginRequest
	display DisplayEvent
	user    userContext
	epoch   uint64
}

type receivePluginResult struct {
	job      receivePluginJob
	content  string
	suppress bool
	err      error
}

func (c *client) receivePluginLoop() {
	for c.ctx.Err() == nil {
		select {
		case <-c.ctx.Done():
			return
		case job := <-c.receiveJobs:
			content, suppress, err := runMessagePlugin(c.ctx, job.plugin, job.request)
			select {
			case c.receiveResults <- receivePluginResult{job: job, content: content, suppress: suppress, err: err}:
			case <-c.ctx.Done():
				return
			}
		}
	}
}

func (c *client) finishReceivePlugin(result receivePluginResult) {
	job := result.job
	active := c.getActiveView()
	if job.epoch != c.receiveEpoch || active == nil || (!active.IsGroup && active.Name != job.display.Chat) || (active.IsGroup && !slices.Contains(active.Children, job.display.Chat)) {
		return
	}
	for _, blocked := range c.config.BlockedUsers {
		if blocked.PubKey == job.display.FullPubKey {
			return
		}
	}
	if result.err != nil {
		c.emit(DisplayEvent{Type: "WARN", Content: fmt.Sprintf("on_receive plugin failed: %s. Displaying the original message.", result.err)})
	} else if result.suppress {
		return
	} else {
		job.display.Content = sanitizeString(truncateString(result.content, MaxMsgLen))
	}
	c.displayReceived(job.display, job.user)
}

func (c *client) displayReceived(display DisplayEvent, user userContext) {
	previous, known := c.userContext.Peek(display.FullPubKey)
	display.RecipientUnchanged = known && previous.nick == user.nick && previous.chat == user.chat && previous.shortPubKey == user.shortPubKey
	c.userContext.Add(display.FullPubKey, user)
	c.emit(display)
}
