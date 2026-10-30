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
	textColor:       tcell.NewHexColor(0xe0e4eb),
	borderColor:     tcell.NewHexColor(0x4f62a8),
	titleColor:      tcell.NewHexColor(0x51cce6),
	inputBgColor:    tcell.NewHexColor(0x243447),
	inputTextColor:  tcell.NewHexColor(0x4de3f2),
	logInfoColor:    tcell.NewHexColor(0x4e97d1),
	logWarnColor:    tcell.NewHexColor(0xf0c45a),
	logErrorColor:   tcell.NewHexColor(0xf06464),
	nickPalette:     []string{"[#56c8e8]", "[#d77de0]", "[#f0c45a]", "[#73bf83]", "[#f27e68]"},
}

var redGoldTheme = &theme{
	backgroundColor: tcell.NewHexColor(0x211719),
	textColor:       tcell.NewHexColor(0xe7e3dc),
	borderColor:     tcell.NewHexColor(0xa75663),
	titleColor:      tcell.NewHexColor(0xffc65c),
	inputBgColor:    tcell.NewHexColor(0x421e24),
	inputTextColor:  tcell.NewHexColor(0xffd34d),
	logInfoColor:    tcell.NewHexColor(0x61b8be),
	logWarnColor:    tcell.NewHexColor(0xf3bf59),
	logErrorColor:   tcell.NewHexColor(0xff7878),
	nickPalette:     []string{"[#e77db2]", "[#69c4c9]", "[#f3bf59]", "[#6ebf7d]", "[#ff8059]"},
}

// Ordered for stable /theme numbers. Themes are immutable after initialization.
var themes = []struct {
	name  string
	style *theme
}{
	{client.DefaultTheme, defaultTheme},
	{"monochrome", monochromeTheme},
	{"blue-gray", blueGrayTheme},
	{"red-gold", redGoldTheme},
}

// handleTheme lists locally but delegates saves to the config owner. The UI
// switches on THEME_UPDATE only after a successful save, never on a failed send.
func (t *tui) handleTheme(arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		var b strings.Builder
		b.WriteString("Themes:\n")
		for i, entry := range themes {
			fmt.Fprintf(&b, "%d. %s", i+1, entry.name)
			if entry.style == t.theme {
				b.WriteString(" (current)")
			}
			b.WriteByte('\n')
		}
		b.WriteString("Usage: /theme <name|number> (alias: /t)")
		t.applyLocalEvent(client.DisplayEvent{Type: "INFO", Content: b.String()})
		return
	}
	for i, entry := range themes {
		if arg == entry.name || arg == strconv.Itoa(i+1) {
			t.submit(client.UserAction{Type: "SET_THEME", Payload: entry.name})
			return
		}
	}
	t.bell()
	t.applyLocalEvent(client.DisplayEvent{Type: "ERROR", Content: "Unknown theme: " + arg + ". Use /theme to list themes."})
}

// restoreTheme runs only on the event loop, for startup and successful saves.
// Unknown names fall back safely without writing back to the configuration.
func (t *tui) restoreTheme(name string) bool {
	selected := defaultTheme
	for _, entry := range themes {
		if name == entry.name {
			selected = entry.style
			break
		}
	}
	if t.theme == selected {
		return false
	}
	t.theme = selected
	t.applyTheme()
	return true
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
	for _, view := range []*tview.TextView{t.logsView.TextView, t.detailsView, t.outputView.TextView, t.hints} {
		view.SetTextStyle(text)
	}
	t.chatList.SetMainTextStyle(text).SetSecondaryTextStyle(text).SetSelectedStyle(selected)
	t.applyInputTheme()
	t.themeColors = th.scrollbackColors()
	// Scrollback records keep canonical default-theme markup; only the widget
	// window has to be recolored, so no history rewrite or cursor reset is
	// needed here.
	t.outputView.invalidateWindow()
	t.logsView.invalidateWindow()
	t.detailsKeySet = false
	t.updateFocusBorders()
	t.updateHints()
}

// applyInputTheme also styles an editor freshly reset by /clear.
func (t *tui) applyInputTheme() {
	th := t.theme
	text := tcell.StyleDefault.Foreground(th.textColor).Background(th.backgroundColor)
	selected := tcell.StyleDefault.Foreground(th.backgroundColor).Background(th.titleColor)
	t.input.SetLabelStyle(text.Foreground(th.titleColor)).
		SetFieldStyle(text.Foreground(th.inputTextColor).Background(th.inputBgColor)).
		SetAutocompleteStyles(th.backgroundColor, text, selected)
}

// scrollbackColors maps the model's fixed default-theme markup at render time.
// Keeping canonical lines lets monochrome -> color restore nickname colors,
// avoids retaining raw events, and keeps mutable themes out of collector/log
// goroutines. Only complete trusted tags match: escaped user text is untouched.
func (th *theme) scrollbackColors() *strings.Replacer {
	ownColor := th.inputTextColor
	if th == monochromeTheme {
		// Black input text needs the white field; chat stays on black.
		ownColor = th.textColor
	}
	var pairs []string
	for _, colors := range [][2]tcell.Color{
		{defaultTheme.titleColor, th.titleColor},
		{defaultTheme.inputTextColor, ownColor},
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
