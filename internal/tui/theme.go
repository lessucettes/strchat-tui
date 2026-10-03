package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// theme holds the color definitions for the application's UI.
type theme struct {
	backgroundColor tcell.Color
	textColor       tcell.Color
	borderColor     tcell.Color
	titleColor      tcell.Color
	inputBgColor    tcell.Color
	inputTextColor  tcell.Color
	logInfoColor    tcell.Color
	logWarnColor    tcell.Color
	logErrorColor   tcell.Color
	nickPalette     []string
}

// defaultTheme is the standard green-on-black theme.
var defaultTheme = &theme{
	backgroundColor: tcell.ColorBlack,
	textColor:       tcell.ColorGainsboro,
	borderColor:     tcell.ColorDarkOliveGreen,
	titleColor:      tcell.ColorLimeGreen,
	inputBgColor:    tcell.NewRGBColor(0, 40, 0),
	inputTextColor:  tcell.ColorLime,
	logInfoColor:    tcell.ColorGrey,
	logWarnColor:    tcell.ColorYellow,
	logErrorColor:   tcell.ColorRed,
	nickPalette: []string{
		"[#33ccff]", // Cyan
		"[#ff00ff]", // Magenta
		"[#ffff00]", // Yellow
		"[#6600ff]", // Purple
		"[#ff6347]", // Red
	},
}

// monochromeTheme is a simple black and white theme for high contrast.
var monochromeTheme = &theme{
	backgroundColor: tcell.ColorBlack,
	textColor:       tcell.ColorWhite,
	borderColor:     tcell.ColorWhite,
	titleColor:      tcell.ColorWhite,
	inputBgColor:    tcell.ColorWhite,
	inputTextColor:  tcell.ColorBlack,
	logInfoColor:    tcell.ColorWhite,
	logWarnColor:    tcell.ColorWhite,
	logErrorColor:   tcell.ColorWhite,
	nickPalette: []string{
		"[white]",
	},
}

var blueGrayTheme = &theme{
	backgroundColor: tcell.NewHexColor(0x171e28),
	textColor:       tcell.NewHexColor(0xcbd5e1),
	borderColor:     tcell.NewHexColor(0x526780),
	titleColor:      tcell.NewHexColor(0x88b9e8),
	inputBgColor:    tcell.NewHexColor(0x243447),
	inputTextColor:  tcell.NewHexColor(0xd6e8fa),
	logInfoColor:    tcell.NewHexColor(0x93a4b8),
	logWarnColor:    tcell.NewHexColor(0xe8cc88),
	logErrorColor:   tcell.NewHexColor(0xf08c96),
	nickPalette:     []string{"[#88b9e8]", "[#b8a1df]", "[#e8cc88]", "[#8dc6c0]", "[#dba5ba]"},
}

var redGoldTheme = &theme{
	backgroundColor: tcell.NewHexColor(0x211719),
	textColor:       tcell.NewHexColor(0xead9c5),
	borderColor:     tcell.NewHexColor(0x995052),
	titleColor:      tcell.NewHexColor(0xe8bd65),
	inputBgColor:    tcell.NewHexColor(0x421e24),
	inputTextColor:  tcell.NewHexColor(0xffdc91),
	logInfoColor:    tcell.NewHexColor(0xb79c90),
	logWarnColor:    tcell.NewHexColor(0xf3bf59),
	logErrorColor:   tcell.NewHexColor(0xff7878),
	nickPalette:     []string{"[#e8bd65]", "[#e796a5]", "[#f3bf59]", "[#c5a5de]", "[#ed9b73]"},
}

// Ordered for stable /theme numbers. Themes are immutable after initialization.
var themes = []struct {
	name  string
	style *theme
}{
	{"default", defaultTheme},
	{"monochrome", monochromeTheme},
	{"blue-gray", blueGrayTheme},
	{"red-gold", redGoldTheme},
}

// handleTheme is local to the event loop: no client action or config write.
func (t *tui) handleTheme(arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		var b strings.Builder
		b.WriteString("Themes (session only):\n")
		for i, entry := range themes {
			fmt.Fprintf(&b, "%d. %s", i+1, entry.name)
			if entry.style == t.theme {
				b.WriteString(" (current)")
			}
			b.WriteByte('\n')
		}
		b.WriteString("Usage: /theme <name|number> (alias: /t)")
		t.applyEvent(client.DisplayEvent{Type: "INFO", Content: b.String()})
		return
	}
	for i, entry := range themes {
		if arg == entry.name || arg == strconv.Itoa(i+1) {
			t.theme = entry.style
			t.applyTheme()
			t.applyEvent(client.DisplayEvent{Type: "INFO", Content: "Theme: " + entry.name})
			return
		}
	}
	t.applyEvent(client.DisplayEvent{Type: "ERROR", Content: "Unknown theme: " + arg + ". Use /theme to list themes."})
}

// applyTheme updates existing widgets on the event loop, not tview's global
// construction defaults. Layout, input, selection and focus remain intact.
func (t *tui) applyTheme() {
	th := t.theme
	for _, box := range []*tview.Box{
		t.mainFlex.Box, t.contentGrid.Box, t.sidebarFlex.Box,
		t.sidebarFlexHorizontal.Box, t.bottomFlex.Box,
		t.maximizedLogsFlex.Box, t.maximizedOutputFlex.Box,
		t.logsView.Box, t.chatList.Box, t.detailsView.Box,
		t.outputView.Box, t.input.Box, t.hints.Box,
	} {
		box.SetBackgroundColor(th.backgroundColor).
			SetBorderColor(th.borderColor).SetTitleColor(th.titleColor)
	}
	text := tcell.StyleDefault.Foreground(th.textColor).Background(th.backgroundColor)
	selected := tcell.StyleDefault.Foreground(th.backgroundColor).Background(th.titleColor)
	for _, view := range []*tview.TextView{t.logsView, t.detailsView, t.outputView, t.hints} {
		view.SetTextStyle(text)
	}
	t.chatList.SetMainTextStyle(text).SetSecondaryTextStyle(text).SetSelectedStyle(selected)
	t.input.SetLabelStyle(text.Foreground(th.titleColor)).
		SetFieldStyle(text.Foreground(th.inputTextColor).Background(th.inputBgColor)).
		SetAutocompleteStyles(th.backgroundColor, text, selected)
	t.themeColors = th.scrollbackColors()
	t.mu.Lock()
	t.outputSynced, t.logsSynced = -2, -2 // Repaint retained canonical lines.
	t.mu.Unlock()
	t.detailsKeySet = false
	t.updateFocusBorders()
	t.updateHints()
}

// scrollbackColors maps the model's fixed default-theme markup at render time.
// Keeping canonical lines lets monochrome -> color restore nickname colors,
// avoids retaining raw events, and keeps mutable themes out of collector/log
// goroutines. Only complete trusted tags match: escaped user text is untouched.
func (th *theme) scrollbackColors() *strings.Replacer {
	var pairs []string
	for _, colors := range [][2]tcell.Color{
		{defaultTheme.titleColor, th.titleColor},
		{defaultTheme.inputTextColor, th.inputTextColor},
		{defaultTheme.logInfoColor, th.logInfoColor},
		{defaultTheme.logWarnColor, th.logWarnColor},
		{defaultTheme.logErrorColor, th.logErrorColor},
	} {
		pairs = append(pairs, colorTag(colors[0]), colorTag(colors[1]),
			colorBoldTag(colors[0]), colorBoldTag(colors[1]))
	}
	for i, tag := range defaultTheme.nickPalette {
		// The default yellow nick and warning share a tag; every colored theme
		// keeps palette entry 2 equal to its warning color for this reason.
		pairs = append(pairs, tag, th.nickPalette[i%len(th.nickPalette)])
	}
	return strings.NewReplacer(pairs...)
}
