package tui

import (
	"slices"
	"strings"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// Conversation selection belongs to the event loop, not to relay or widget focus.
func (t *tui) openInfo() {
	if t.plan.mode == modeTiny {
		t.infoTemporary = true
		t.rebuildLayout(t.plan)
		s := t.snapshot()
		if s.activeIndex >= 0 && s.activeIndex < len(s.views) {
			t.chatList.SetCurrentItem(s.activeIndex)
		}
	}
	t.renderDetails(t.snapshot())
	t.focusIfVisible(t.detailsView)
}

func activeView(s modelSnapshot) *client.View {
	if s.activeIndex < 0 || s.activeIndex >= len(s.views) {
		return nil
	}
	return &s.views[s.activeIndex]
}

func (t *tui) conversationChat(s modelSnapshot) string {
	view := activeView(s)
	if view == nil {
		return ""
	}
	if !view.IsGroup {
		return view.Name
	}
	if slices.Contains(view.Children, view.SelectedChat) {
		return view.SelectedChat
	}
	if len(view.Children) > 0 {
		return view.Children[0]
	}
	return ""
}

// pruneConversationFocusLocked requires t.mu and never touches widgets.
func (t *tui) pruneConversationFocusLocked(views []client.View) {
	for group, chat := range t.groupFocus {
		valid := false
		for _, view := range views {
			if view.IsGroup && view.Name == group && slices.Contains(view.Children, chat) {
				valid = true
				break
			}
		}
		if !valid {
			delete(t.groupFocus, group)
		}
	}
	for _, view := range views {
		if view.IsGroup && len(view.Children) > 0 && t.groupFocus[view.Name] == "" {
			chat := view.SelectedChat
			if !slices.Contains(view.Children, chat) {
				chat = view.Children[0]
			}
			t.groupFocus[view.Name] = chat
		}
	}
}

func (t *tui) handleInfoKeys(event *tcell.EventKey) *tcell.EventKey {
	s := t.snapshot()
	view := activeView(s)
	// Browsing an inactive group is only a preview, never a routing action.
	if view == nil || !view.IsGroup || t.chatList.GetCurrentItem() != s.activeIndex || len(view.Children) == 0 {
		return event
	}
	index := slices.Index(view.Children, t.conversationChat(s))
	switch event.Key() {
	case tcell.KeyDown:
		index++
	case tcell.KeyUp:
		if index < 0 {
			index = len(view.Children)
		}
		index--
	case tcell.KeyHome:
		index = 0
	case tcell.KeyEnd:
		index = len(view.Children) - 1
	default:
		return event
	}
	if index < 0 || index >= len(view.Children) {
		t.bell()
		return nil
	}
	chat := view.Children[index]
	if chat != t.conversationChat(s) {
		if !t.submit(client.UserAction{Type: "SELECT_GROUP_CHAT", View: view.Name, Chat: chat}) {
			return nil
		}
		t.mu.Lock()
		for _, current := range t.views {
			if current.IsGroup && current.Name == view.Name && slices.Contains(current.Children, chat) {
				t.groupFocus[view.Name] = chat
				break
			}
		}
		t.mu.Unlock()
	}
	t.infoReveal = true
	t.render()
	return nil
}

// Reveal after layout has assigned the real rectangle, not using the previous
// hidden/sidebar height when Info has just been opened on a tiny terminal.
func (t *tui) installInfoScroll() {
	lastWidth, lastHeight := 0, 0
	t.detailsView.SetDrawFunc(func(_ tcell.Screen, x, y, width, height int) (int, int, int, int) {
		x, y, width, height = x+1, y+1, max(0, width-2), max(0, height-2)
		if t.infoReveal || width != lastWidth || height != lastHeight {
			s := t.snapshot()
			view := activeView(s)
			if view != nil && view.IsGroup && t.chatList.GetCurrentItem() == s.activeIndex && height > 0 {
				index := slices.Index(view.Children, t.conversationChat(s))
				row, _ := t.detailsView.GetScrollOffset()
				if index >= 0 && index < row {
					t.detailsView.ScrollTo(index, 0)
				} else if index >= row+height {
					t.detailsView.ScrollTo(index-height+1, 0)
				}
			}
			t.infoReveal = false
		}
		lastWidth, lastHeight = width, height
		return x, y, width, height
	})
}

func (t *tui) renderConversationTitles(s modelSnapshot) {
	t.applyTitles(t.plan)
	index := t.chatList.GetCurrentItem()
	if index >= 0 && index < len(s.views) && s.views[index].IsGroup {
		t.detailsView.SetTitle(titleInfo)
	}
	view := activeView(s)
	if view == nil || !view.IsGroup {
		return
	}
	t.outputView.SetTitle(titleMessages)
	chat := t.conversationChat(s)
	text := strings.TrimSpace(t.input.GetText())
	if strings.HasPrefix(text, "@") {
		query := client.MessageTarget{Text: text, View: view.Name, DefaultChat: chat}
		if !t.targetRequested || t.targetQuery != query || t.targetQueryRev != s.recipientRev {
			if t.submit(client.UserAction{Type: "RESOLVE_MESSAGE_TARGET", Payload: text, View: view.Name, Chat: chat}) {
				t.targetQuery, t.targetQueryRev, t.targetRequested = query, s.recipientRev, true
			}
		}
	}
	t.input.SetTitle(titleInput)
}

func (t *tui) currentMessageTarget(s modelSnapshot) (client.MessageTarget, bool) {
	view := activeView(s)
	result := s.messageTarget
	if view == nil || result.Text != strings.TrimSpace(t.input.GetText()) || result.View != view.Name || result.DefaultChat != t.conversationChat(s) {
		return result, false
	}
	return result, result.Error != "" || slices.Contains(view.Children, result.Chat)
}
