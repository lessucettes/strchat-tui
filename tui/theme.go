// tui/theme.go
package tui

import "github.com/gdamore/tcell/v2"

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
	textColor:      tcell.ColorWhite,
	borderColor:    tcell.ColorWhite,
	titleColor:     tcell.ColorWhite,
	inputBgColor:   tcell.ColorWhite,
	inputTextColor: tcell.ColorBlack,
	logInfoColor:   tcell.ColorWhite,
	logWarnColor:   tcell.ColorWhite,
	logErrorColor:  tcell.ColorWhite,
	nickPalette: []string{
		"[white]",
	},
}
