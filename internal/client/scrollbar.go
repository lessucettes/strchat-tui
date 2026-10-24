package client

import (
	"fmt"
	"strings"
)

// ScrollbarPosition controls whether pane scrollbars are drawn and on which
// side. Configuration defaults to ScrollbarRight; explicit zero means off.
type ScrollbarPosition int

const (
	ScrollbarOff ScrollbarPosition = iota
	ScrollbarRight
	ScrollbarLeft
)

func (p ScrollbarPosition) valid() bool {
	return p >= ScrollbarOff && p <= ScrollbarLeft
}

func (p ScrollbarPosition) String() string {
	switch p {
	case ScrollbarOff:
		return "off"
	case ScrollbarRight:
		return "right"
	case ScrollbarLeft:
		return "left"
	default:
		return "unknown"
	}
}

// setScrollbar runs on the client owner goroutine. No argument silently cycles
// modes; explicitly re-selecting the current mode shows help without saving.
// Changes are persisted before updating live state, matching setTheme.
func (c *client) setScrollbar(raw string) {
	payload := strings.TrimSpace(raw)
	var next ScrollbarPosition
	switch payload {
	case "":
		next = (c.config.Scrollbar + 1) % (ScrollbarLeft + 1)
	case "0":
		next = ScrollbarOff
	case "1":
		next = ScrollbarRight
	case "2":
		next = ScrollbarLeft
	default:
		c.emit(DisplayEvent{Type: "ERROR", Content: "Invalid scrollbar position. Use 0 (off), 1 (right), or 2 (left)."})
		return
	}

	if next == c.config.Scrollbar {
		c.emit(DisplayEvent{Type: "INFO", Content: fmt.Sprintf(
			"Scrollbar: %s (%d)\nOptions: 0 off, 1 right (default), 2 left\nUsage: /scrollbar [0|1|2] (Alias: /sb)\nWithout arguments: cycle 0 -> 1 -> 2 -> 0.",
			c.config.Scrollbar.String(), c.config.Scrollbar)})
		return
	}

	updated := *c.config
	updated.Scrollbar = next
	if err := updated.save(); err != nil {
		c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Failed to save scrollbar. The previous setting is unchanged. Reason: %q.", err)})
		return
	}
	c.config.Scrollbar = next
	c.emit(DisplayEvent{Type: "SCROLLBAR_UPDATE", Payload: next})
	if payload != "" {
		c.emit(DisplayEvent{Type: "INFO", Content: "Scrollbar: " + next.String()})
	}
}
