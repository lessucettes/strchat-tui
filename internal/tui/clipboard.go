package tui

import (
	"strings"

	"github.com/atotto/clipboard"
)

func stripTviewTags(s string) string {
	s = strings.TrimSpace(s)
	for strings.Contains(s, "[") {
		i := strings.IndexByte(s, '[')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i+1:], ']')
		if j < 0 {
			break
		}
		s = s[:i] + s[i+j+2:]
	}
	return strings.TrimSpace(s)
}

func isCopyRune(r rune) bool {
	switch r {
	case 'c', 'C', 'с', 'С': // latin C/c, cyrillic es
		return true
	default:
		return false
	}
}

// copyFocusedSelectionToClipboard copies the focused panel’s selection (list row or log line).
func (t *tui) copyFocusedSelectionToClipboard() bool {
	f := t.app.GetFocus()
	var text string

	switch f {
	case t.chatList:
		cur := t.chatList.GetCurrentItem()
		if cur < 0 || cur >= len(t.chatListItems) {
			return true
		}
		vi := t.chatListItems[cur].viewIndex
		if vi < 0 || vi >= len(t.views) {
			return true
		}
		text = t.views[vi].Name

	case t.userList:
		cur := t.userList.GetCurrentItem()
		if cur < 0 || len(t.chatUsers) == 0 || cur >= len(t.chatUsers) {
			return true
		}
		u := t.chatUsers[cur]
		sh := u.ShortPubKey
		if sh == "" && len(u.PubKey) >= 4 {
			sh = u.PubKey[len(u.PubKey)-4:]
		}
		text = u.Nick + "#" + sh

	case t.output:
		i := t.output.GetCurrentItem()
		if i < 0 || i >= t.output.GetItemCount() {
			return true
		}
		main, _ := t.output.GetItemText(i)
		text = stripTviewTags(main)

	case t.detailsView:
		i := t.detailsView.GetCurrentItem()
		if i < 0 || i >= t.detailsView.GetItemCount() {
			return true
		}
		main, _ := t.detailsView.GetItemText(i)
		text = stripTviewTags(main)

	case t.logs:
		raw := t.logs.GetText(true)
		raw = strings.TrimRight(raw, "\n")
		if raw == "" {
			return true
		}
		lines := strings.Split(raw, "\n")
		row, _ := t.logs.GetScrollOffset()
		if row < 0 {
			row = 0
		}
		if row >= len(lines) {
			row = len(lines) - 1
		}
		text = stripTviewTags(lines[row])

	default:
		return false
	}

	if text != "" {
		_ = clipboard.WriteAll(text)
	}
	return true
}
