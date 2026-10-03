package client

import "fmt"

const DefaultTheme = "default"

func validTheme(name string) bool {
	switch name {
	case DefaultTheme, "monochrome", "blue-gray", "red-gold":
		return true
	}
	return false
}

// setTheme runs on the client owner goroutine. Confirm only after the atomic
// save succeeds; a failed save leaves both the current theme and config intact.
func (c *client) setTheme(name string) {
	if !validTheme(name) {
		c.emit(DisplayEvent{Type: "ERROR", Content: "Unknown theme. Use /theme to list themes."})
		return
	}
	next := *c.config
	next.Theme = name
	if err := next.save(); err != nil {
		c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Failed to save theme: %v", err)})
		return
	}
	c.config.Theme = name
	c.emit(DisplayEvent{Type: "THEME_UPDATE", Content: name})
	c.emit(DisplayEvent{Type: "INFO", Content: "Theme: " + name})
}
