// tui/theme.go
package tui

import "github.com/gdamore/tcell/v2"

// Theme holds the color definitions for the application's UI.
type Theme struct {
	BackgroundColor tcell.Color
	TextColor       tcell.Color
	BorderColor     tcell.Color
	TitleColor      tcell.Color
	InputBgColor    tcell.Color
	InputTextColor  tcell.Color
	NickPalette     []string
}

// DefaultTheme is the standard green-on-black theme.
var DefaultTheme = &Theme{
	BackgroundColor: tcell.ColorBlack,
	TextColor:       tcell.ColorGainsboro,
	BorderColor:     tcell.ColorDarkOliveGreen,
	TitleColor:      tcell.ColorLimeGreen,
	InputBgColor:    tcell.NewRGBColor(0, 40, 0),
	InputTextColor:  tcell.ColorLime,
	NickPalette: []string{
		"[#00ff00]", // Green
		"[#33ccff]", // Cyan
		"[#ff00ff]", // Magenta
		"[#ffff00]", // Yellow
		"[#6600ff]", // Purple
		"[#ff6347]", // Red
	},
}

// MonochromeTheme is a simple black and white theme for high contrast.
var MonochromeTheme = &Theme{
	BackgroundColor: tcell.ColorBlack,
	TextColor:       tcell.ColorWhite,
	BorderColor:     tcell.ColorWhite,
	TitleColor:      tcell.ColorWhite,
	InputBgColor:    tcell.ColorWhite,
	InputTextColor:  tcell.ColorBlack,
	NickPalette: []string{
		"[white]",
	},
}
