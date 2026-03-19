package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// isMetadataBlock returns true for [id time] content so we count it as visible in wrap.
func isMetadataBlock(inner string) bool {
	if len(inner) < 5 || len(inner) > 25 {
		return false
	}
	hasColon := false
	for _, r := range inner {
		switch {
		case r >= '0' && r <= '9', r == ' ', r == ':':
			if r == ':' {
				hasColon = true
			}
		default:
			return false
		}
	}
	return hasColon
}

// wrapTviewDisplay splits styled text into lines that fit maxCells terminal columns.
// Segments in square brackets (tview color tags) contribute 0 to width.
func wrapTviewDisplay(s string, maxCells int) []string {
	if maxCells < 20 {
		maxCells = 60
	}
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return []string{""}
	}
	var out []string
	rem := strings.TrimLeft(s, " ")
	for len(rem) > 0 {
		line, n := wrapOneLine(rem, maxCells)
		line = strings.TrimSpace(line)
		if line != "" || len(out) == 0 {
			out = append(out, line)
		}
		if n >= len(rem) {
			break
		}
		rem = strings.TrimLeft(rem[n:], " ")
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func wrapOneLine(s string, maxCells int) (line string, consumed int) {
	if s == "" {
		return "", 0
	}
	col := 0
	lastSpace := -1
	lastSpaceCol := 0
	i := 0
	for i < len(s) {
		if s[i] == '\n' {
			return strings.TrimRight(s[:i], " "), i + 1
		}
		if s[i] == '[' {
			j := strings.IndexByte(s[i+1:], ']')
			if j >= 0 {
				inner := s[i+1 : i+1+j]
				// [id time] metadata block: count as visible so it wraps
				if isMetadataBlock(inner) {
					col++
					i++
					for i < len(s) && s[i] != ']' {
						r, rw := utf8.DecodeRuneInString(s[i:])
						if r != utf8.RuneError {
							col += runewidth.RuneWidth(r)
						}
						i += rw
					}
					if i < len(s) {
						col++
						i++
					}
					continue
				}
				i += j + 2
				continue
			}
		}
		r, rw := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError {
			i++
			continue
		}
		cw := runewidth.RuneWidth(r)
		if r == ' ' || r == '\t' {
			lastSpace = i
			lastSpaceCol = col
		}
		if col+cw > maxCells && col > 0 {
			if lastSpace >= 0 && lastSpaceCol >= maxCells/4 {
				return strings.TrimRight(s[:lastSpace], " "), lastSpace + 1
			}
			return s[:i], i
		}
		col += cw
		i += rw
	}
	return s, len(s)
}

func (t *tui) messagesWrapColumns(screenW int) int {
	if screenW < 30 {
		return 24
	}
	if t.narrowMode {
		return max(24, screenW-4)
	}
	// Wide: messages column ~ full width minus right sidebar (30) and borders.
	return max(24, screenW-36)
}

func (t *tui) rebuildMessagesOutputPreservingSelection() {
	if t.output == nil {
		return
	}
	cur := t.output.GetCurrentItem()
	msgIdx := -1
	if cur >= 0 && cur < len(t.outputRowToMsg) {
		msgIdx = t.outputRowToMsg[cur]
	}
	t.output.Clear()
	t.outputRowToMsg = nil
	w := t.messagesCachedWrapW
	if w < 20 {
		w = 60
	}
	for mi, om := range t.outputMessages {
		disp := om.RawDisplay
		if disp == "" && strings.TrimSpace(om.Content) != "" {
			disp = "-- " + strings.TrimSpace(om.Content)
		}
		if disp == "" {
			continue
		}
		lines := wrapTviewDisplay(disp, w)
		if om.MentionToMe {
			const rowFg = "#fff8dc"
			const rowBg = "#5c4318"
			for i := range lines {
				lines[i] = fmt.Sprintf("[%s:%s:-]%s[-]", rowFg, rowBg, lines[i])
			}
		} else if om.IsOwnMessage {
			t.applyOwnMessageMultilineGreen(lines)
		}
		for _, ln := range lines {
			t.output.AddItem(ln, "", 0, nil)
			t.outputRowToMsg = append(t.outputRowToMsg, mi)
		}
	}
	if t.output.GetItemCount() == 0 {
		return
	}
	if msgIdx >= 0 {
		for i, mid := range t.outputRowToMsg {
			if mid == msgIdx {
				t.output.SetCurrentItem(i)
				return
			}
		}
	}
	t.output.SetCurrentItem(t.output.GetItemCount() - 1)
}
