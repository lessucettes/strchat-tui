package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// colorHexTag builds a tview color tag; [%s] with tcell.Color breaks border titles.
func colorHexTag(c tcell.Color) string {
	r, g, b := c.RGB()
	return fmt.Sprintf("[#%02x%02x%02x]", uint8(r), uint8(g), uint8(b))
}

func colorHexTagBold(c tcell.Color) string {
	r, g, b := c.RGB()
	return fmt.Sprintf("[#%02x%02x%02x::b]", uint8(r), uint8(g), uint8(b))
}

// updateChatList refreshes the chat list view, indicating the active and selected chats.
func (t *tui) updateChatList() {
	currentItem := t.chatList.GetCurrentItem()
	t.chatList.Clear()
	t.chatListItems = nil

	if currentItem < 0 {
		currentItem = 0
	}

	for i, view := range t.views {
		// Skip legacy DM-prefixed views (they were created by old DM logic).
		if strings.HasPrefix(view.Name, "DM-") {
			continue
		}
		var prefix string
		isActive := i == t.activeViewIndex
		isSelected := t.selectedForGroup[view.Name]

		if isActive && isSelected && !view.IsGroup {
			prefix = "⊛"
		} else if isActive {
			prefix = "▶"
		} else if isSelected {
			prefix = "⊕"
		} else {
			prefix = " "
		}

		viewName := view.Name
		if view.PoW > 0 {
			viewName = fmt.Sprintf("%s [PoW:%d]", view.Name, view.PoW)
		}

		t.chatListItems = append(t.chatListItems, chatListItem{
			viewIndex: i,
		})

		t.chatList.AddItem(fmt.Sprintf(" %s %s", prefix, viewName), "", 0, nil)
	}

	targetIndex := 0
	if t.activeViewIndex >= 0 {
		targetIndex = t.activeViewIndex
	}

	if targetIndex < 0 {
		targetIndex = 0
	}
	if targetIndex >= len(t.chatListItems) {
		targetIndex = len(t.chatListItems) - 1
	}
	if targetIndex >= 0 {
		t.chatList.SetCurrentItem(targetIndex)
	} else if currentItem >= 0 {
		t.chatList.SetCurrentItem(currentItem)
	}
	t.refreshChatListTitle()
}

func (t *tui) refreshChatListTitle() {
	n := len(t.chatListItems)
	if t.narrowMode {
		t.chatList.SetTitle(fmt.Sprintf("Chats C Total: %d", n))
	} else {
		t.chatList.SetTitle(fmt.Sprintf("Chats (Alt+C) Total: %d", n))
	}
	t.chatList.SetTitleAlign(tview.AlignLeft)
}

// refreshUserListTitle sets the users panel title to the count shown in that list.
func (t *tui) refreshUserListTitle() {
	n := len(t.chatUsers)
	if t.narrowMode {
		t.userList.SetTitle(fmt.Sprintf("Users Online: %d", n))
	} else {
		t.userList.SetTitle(fmt.Sprintf("Users (Alt+U) Online: %d", n))
	}
	t.userList.SetTitleAlign(tview.AlignLeft)
}

func (t *tui) refreshRelaysPanelChrome() {
	if t.relaysPanel == nil {
		return
	}
	n := len(t.relays)
	g := colorHexTag(t.theme.inputTextColor)
	if t.narrowMode {
		t.relaysPanel.SetTitle(fmt.Sprintf("Relays R Total: %d", n))
	} else {
		t.relaysPanel.SetTitle(fmt.Sprintf("Relays (Alt+R) Total: %d", n))
	}
	t.relaysPanel.SetTitleAlign(tview.AlignLeft)
	if t.relaysFooter != nil {
		r := colorHexTag(t.theme.logErrorColor)
		t.relaysFooter.SetText(fmt.Sprintf("Online: %s%d[-]\nDown: %s%d[-]", g, t.relaysUpCount, r, t.relaysDownCount))
	}
}

// updateUserList refreshes the users panel for the currently active view.
func (t *tui) updateUserList() {
	currentItem := t.userList.GetCurrentItem()
	t.userList.Clear()

	if len(t.chatUsers) == 0 {
		t.refreshUserListTitle()
		return
	}

	sort.SliceStable(t.chatUsers, func(i, j int) bool {
		if t.chatUsers[i].Nick == t.chatUsers[j].Nick {
			return t.chatUsers[i].ShortPubKey < t.chatUsers[j].ShortPubKey
		}
		return t.chatUsers[i].Nick < t.chatUsers[j].Nick
	})

	for idx, u := range t.chatUsers {
		colorTag := t.colorTagForPubkey(u.PubKey)
		short := u.ShortPubKey
		if short == "" {
			if len(u.PubKey) >= 4 {
				short = u.PubKey[len(u.PubKey)-4:]
			} else {
				short = "????"
			}
		}
		t.userList.AddItem(fmt.Sprintf("%2d %s%s[-]#%s", idx+1, colorTag, u.Nick, short), "", 0, nil)
	}

	if currentItem >= 0 && currentItem < t.userList.GetItemCount() {
		t.userList.SetCurrentItem(currentItem)
	} else {
		t.userList.SetCurrentItem(0)
	}
	t.refreshUserListTitle()
}

func (t *tui) activeViewColorKey() string {
	if t.activeViewIndex < 0 || t.activeViewIndex >= len(t.views) {
		return ""
	}
	return t.views[t.activeViewIndex].Name
}

// ensureParticipantColorsFromUsers assigns colors only for pubkeys not yet seen in this view.
func (t *tui) ensureParticipantColorsFromUsers() {
	for pk := range t.chatUsersByPubKey {
		if pk != "" {
			t.assignNewParticipantColor(pk)
		}
	}
}

func pickDistinctHue(used []float64) float64 {
	if len(used) == 0 {
		return 0
	}
	bestH := 0.0
	bestMin := -1.0
	for step := 0; step < 72; step++ {
		cand := float64(step) * 5.0
		minD := 360.0
		for _, h := range used {
			d := math.Abs(cand - h)
			if d > 180 {
				d = 360 - d
			}
			if d < minD {
				minD = d
			}
		}
		if minD > bestMin {
			bestMin = minD
			bestH = cand
		}
	}
	return bestH
}

// assignNewParticipantColor gives a new speaker a hue as far as possible from others in this view.
func (t *tui) assignNewParticipantColor(pubkey string) {
	if pubkey == "" {
		return
	}
	vk := t.activeViewColorKey()
	if vk == "" {
		return
	}
	t.chatColorMu.Lock()
	defer t.chatColorMu.Unlock()
	if t.participantColorByView[vk] == nil {
		t.participantColorByView[vk] = make(map[string]string)
		t.participantHueByView[vk] = make(map[string]float64)
	}
	tags := t.participantColorByView[vk]
	hues := t.participantHueByView[vk]
	if _, ok := tags[pubkey]; ok {
		return
	}
	// Same @nick#hash was colored before this pubkey appeared — reuse that color.
	if u, ok := t.chatUsersByPubKey[pubkey]; ok && u.Nick != "" && u.ShortPubKey != "" {
		sh := strings.ToLower(strings.TrimSpace(u.ShortPubKey))
		if len(sh) > 4 {
			sh = sh[len(sh)-4:]
		}
		if len(sh) == 4 {
			refKey := "m/" + strings.ToLower(strings.TrimSpace(u.Nick)) + "#" + sh
			if tag, ok2 := tags[refKey]; ok2 {
				tags[pubkey] = tag
				hues[pubkey] = hues[refKey]
				return
			}
		}
	}
	used := make([]float64, 0, len(hues))
	for _, h := range hues {
		used = append(used, h)
	}
	h := pickDistinctHue(used)
	idx := len(hues)
	s := 0.52 + float64(idx%6)*0.055
	v := 0.80 + float64((idx/6)%4)*0.04
	r, g, b := hsvToRGBBytes(h, s, v)
	hues[pubkey] = h
	tags[pubkey] = fmt.Sprintf("[#%02x%02x%02x]", r, g, b)
}

// colorTagForMentionNickHash colors @nick#xxxx before that user is in the roster
// (e.g. A tags B before B’s first message). When B appears, assignNewParticipantColor
// reuses this color so mentions match B’s nick color.
func (t *tui) colorTagForMentionNickHash(nick, hex4 string) string {
	nick = strings.TrimSpace(nick)
	hex4 = strings.TrimSpace(strings.ToLower(hex4))
	if nick == "" || len(hex4) < 4 {
		return "[#aaaaaa]"
	}
	if len(hex4) > 4 {
		hex4 = hex4[len(hex4)-4:]
	}
	refKey := "m/" + strings.ToLower(nick) + "#" + hex4
	vk := t.activeViewColorKey()
	if vk == "" {
		return pubkeyToNickColorTag(refKey)
	}
	t.chatColorMu.Lock()
	defer t.chatColorMu.Unlock()
	if t.participantColorByView[vk] == nil {
		t.participantColorByView[vk] = make(map[string]string)
		t.participantHueByView[vk] = make(map[string]float64)
	}
	tags := t.participantColorByView[vk]
	hues := t.participantHueByView[vk]
	if tag, ok := tags[refKey]; ok {
		return tag
	}
	used := make([]float64, 0, len(hues))
	for _, h := range hues {
		used = append(used, h)
	}
	h := pickDistinctHue(used)
	idx := len(hues)
	s := 0.52 + float64(idx%6)*0.055
	v := 0.80 + float64((idx/6)%4)*0.04
	r, g, b := hsvToRGBBytes(h, s, v)
	hues[refKey] = h
	tag := fmt.Sprintf("[#%02x%02x%02x]", r, g, b)
	tags[refKey] = tag
	return tag
}

func (t *tui) colorTagForPubkey(pubkey string) string {
	if pubkey == "" {
		return "[#aaaaaa]"
	}
	vk := t.activeViewColorKey()
	t.chatColorMu.RLock()
	if vk != "" && t.participantColorByView[vk] != nil {
		if tag, ok := t.participantColorByView[vk][pubkey]; ok {
			t.chatColorMu.RUnlock()
			return tag
		}
	}
	t.chatColorMu.RUnlock()
	if vk != "" {
		t.assignNewParticipantColor(pubkey)
		t.chatColorMu.RLock()
		var tag string
		if m := t.participantColorByView[vk]; m != nil {
			tag = m[pubkey]
		}
		t.chatColorMu.RUnlock()
		if tag != "" {
			return tag
		}
	}
	return pubkeyToNickColorTag(pubkey)
}

// updateDetailsView refreshes the relays panel list (group members or per-relay rows).
func (t *tui) updateDetailsView() {
	prev := 0
	if t.detailsView.GetItemCount() > 0 {
		prev = t.detailsView.GetCurrentItem()
	}

	t.refreshRelaysPanelChrome()
	t.detailsView.Clear()

	if t.pullingStatus != "" {
		t.detailsView.AddItem(
			fmt.Sprintf("%sPULLING %s[-]", colorHexTagBold(t.theme.titleColor), t.pullingStatus),
			"", 0, nil)
	}

	if t.chatList.GetItemCount() == 0 || len(t.chatListItems) == 0 {
		t.detailsView.AddItem("— select a chat —", "", 0, nil)
		t.detailsView.SetCurrentItem(0)
		return
	}
	currentIndex := t.chatList.GetCurrentItem()
	if currentIndex >= len(t.chatListItems) || currentIndex < 0 {
		t.detailsView.AddItem("— select a chat —", "", 0, nil)
		t.detailsView.SetCurrentItem(0)
		return
	}

	item := t.chatListItems[currentIndex]
	var selectedView *client.View
	if item.viewIndex >= 0 && item.viewIndex < len(t.views) {
		selectedView = &t.views[item.viewIndex]
	}

	if selectedView != nil && selectedView.IsGroup {
		if len(selectedView.Children) == 0 {
			t.detailsView.AddItem("(no channels)", "", 0, nil)
		} else {
			for _, child := range selectedView.Children {
				t.detailsView.AddItem(child, "", 0, nil)
			}
		}
	} else {
		relays := append([]client.RelayInfo(nil), t.relays...)
		sort.SliceStable(relays, func(i, j int) bool {
			return relays[i].URL < relays[j].URL
		})

		if len(relays) == 0 {
			t.detailsView.AddItem(fmt.Sprintf("%sNot connected[-]", colorHexTag(t.theme.logInfoColor)), "", 0, nil)
		} else {
			for _, r := range relays {
				var statusColor tcell.Color
				var symbol string
				switch {
				case !r.Connected:
					statusColor = t.theme.logErrorColor
					symbol = "×"
				case r.Latency > 750*time.Millisecond:
					statusColor = t.theme.logWarnColor
					symbol = "●"
				default:
					statusColor = t.theme.titleColor
					symbol = "●"
				}
				host := strings.TrimPrefix(strings.TrimPrefix(r.URL, "wss://"), "ws://")
				if runewidth.StringWidth(host) > 24 {
					host = runewidth.Truncate(host, 20, "…")
				}
				t.detailsView.AddItem(fmt.Sprintf("%s%s[-] %s", colorHexTag(statusColor), symbol, host), "", 0, nil)
			}
		}
	}

	n := t.detailsView.GetItemCount()
	if n > 0 {
		if prev >= n {
			prev = n - 1
		}
		if prev < 0 {
			prev = 0
		}
		t.detailsView.SetCurrentItem(prev)
	}
}

// updateInputLabel sets the prompt label for the input field, including the user's nick.
func (t *tui) updateInputLabel() {
	youHash := ""
	if t.selfShortPubKey != "" {
		youHash = t.selfShortPubKey
	}

	if t.nick != "" {
		if youHash != "" {
			t.input.SetLabel(fmt.Sprintf("%s#%s > ", t.nick, youHash))
		} else {
			t.input.SetLabel(fmt.Sprintf("%s > ", t.nick))
		}
	} else {
		t.input.SetLabel("> ")
	}
	t.updateInputTitle()
}

// updateInputTitle shows reply target in the input frame title when replying.
func (t *tui) updateInputTitle() {
	if t.pendingReply == nil {
		if t.narrowMode {
			t.input.SetTitle(titleInputShort)
		} else {
			t.input.SetTitle(titleInput)
		}
		return
	}
	ref := fmt.Sprintf("@%s#%s", t.pendingReply.Nick, t.pendingReply.ShortPubKey)
	if t.narrowMode {
		t.input.SetTitle(fmt.Sprintf("%s Reply %s Alt+Q", titleInputShort, ref))
	} else {
		t.input.SetTitle(fmt.Sprintf("%s Reply to %s: (Alt+Q) to cancel", titleInput, ref))
	}
}

// updateFocusBorders changes widget border colors to highlight the focused element.
func (t *tui) updateFocusBorders() {
	currentFocus := t.app.GetFocus()
	unfocusedColor := tview.Styles.BorderColor
	focusedColor := tview.Styles.TitleColor

	components := map[tview.Primitive]bool{
		t.logs:        false,
		t.chatList:    false,
		t.userList:    false,
		t.relaysPanel: false,
		t.output:      false,
		t.input:       false,
	}

	if _, ok := components[currentFocus]; ok {
		components[currentFocus] = true
	}
	relaysFocused := currentFocus == t.detailsView
	t.logs.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.logs]])
	t.chatList.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.chatList]])
	t.userList.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.userList]])
	t.relaysPanel.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[relaysFocused])
	t.output.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.output]])
	t.input.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.input]])

	if t.logsMaxList != nil {
		logsMaxFocused := t.logsMaximized && currentFocus == t.logsMaxList
		t.logsMaxList.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[logsMaxFocused])
		if logsMaxFocused {
			t.logsMaxList.SetSelectedBackgroundColor(t.theme.borderColor)
		} else {
			t.logsMaxList.SetSelectedBackgroundColor(t.theme.backgroundColor)
		}
	}

	// Only visually highlight the selected message when messages are focused.
	if t.app.GetFocus() == t.output {
		t.output.SetSelectedBackgroundColor(t.theme.borderColor)
	} else {
		t.output.SetSelectedBackgroundColor(t.theme.backgroundColor)
	}

	// Only visually highlight the selected user when Users panel is focused.
	if t.app.GetFocus() == t.userList {
		t.userList.SetSelectedBackgroundColor(t.theme.borderColor)
	} else {
		t.userList.SetSelectedBackgroundColor(t.theme.backgroundColor)
	}

	if t.app.GetFocus() == t.detailsView {
		t.detailsView.SetSelectedBackgroundColor(t.theme.borderColor)
	} else {
		t.detailsView.SetSelectedBackgroundColor(t.theme.backgroundColor)
	}
}

// updateHints displays context-sensitive hints for the user.
func (t *tui) updateHints() {
	var hintText string
	highlight := t.theme.titleColor
	baseHints := fmt.Sprintf("[%[1]s]Alt+...[-]: Focus", highlight)

	if t.app.GetFocus() == t.output {
		t.output.SetSelectedBackgroundColor(t.theme.borderColor)
	} else {
		t.output.SetSelectedBackgroundColor(t.theme.backgroundColor)
	}
	if t.app.GetFocus() == t.detailsView {
		t.detailsView.SetSelectedBackgroundColor(t.theme.borderColor)
	} else {
		t.detailsView.SetSelectedBackgroundColor(t.theme.backgroundColor)
	}

	if t.logsMaximized {
		hintText = fmt.Sprintf("[%[1]s]`[-]: Back | [%[1]s]↑/↓ j/k[-]: Select | [%[1]s]c[-]: Copy", highlight)
	} else if t.outputMaximized {
		hintText = fmt.Sprintf("[%[1]s]`[-]: Restore | [%[1]s]↑/↓[-]: Scroll", highlight)
	} else {
		switch t.app.GetFocus() {
		case t.input:
			if t.pendingReply != nil {
				hintText = fmt.Sprintf("[%[1]s]Enter[-]: Send reply | [%[1]s]Alt+Q[-]: Cancel reply | [%[1]s]Ctrl+P/N[-]: History | %s", highlight, baseHints)
			} else {
				hintText = fmt.Sprintf("[%[1]s]Enter[-]: Send | [%[1]s]Ctrl+P/N[-]: History | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
			}
		case t.output:
			hintText = fmt.Sprintf("[%[1]s]c[-]: Copy | [%[1]s]Enter[-]: Reply | [%[1]s]`[-]: Max | [%[1]s]↑/↓[-]: Scroll | %s", highlight, baseHints)
		case t.detailsView:
			hintText = fmt.Sprintf("[%[1]s]c[-]: Copy | [%[1]s]↑/↓[-]: Select | [%[1]s]Tab/Shift+Tab[-]: Cycle | %s", highlight, baseHints)
		case t.userList:
			hintText = fmt.Sprintf("[%[1]s]c[-]: Copy | [%[1]s]Enter[-]: Reply | [%[1]s]Tab/Shift+Tab[-]: Cycle | %s", highlight, baseHints)
		case t.chatList:
			hintText = fmt.Sprintf("[%[1]s]c[-]: Copy | [%[1]s]Space[-]: Toggle | [%[1]s]Enter[-]: Open | [%[1]s]Del[-]: Del | %s", highlight, baseHints)
		case t.logs:
			hintText = fmt.Sprintf("[%[1]s]c[-]: Copy line | [%[1]s]`[-]: Max | [%[1]s]↑/↓[-]: Scroll | %s", highlight, baseHints)
		default:
			hintText = baseHints
		}
	}
	t.hints.SetText(hintText)
}
