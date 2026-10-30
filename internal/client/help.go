package client

import (
	"fmt"
	"strings"
)

// Keep each category in one place so full and focused help cannot drift apart.
var helpCategories = []struct{ name, commands string }{
	{"chats", `  /join <chat> [chat...]  (Alias: /j)
    Join one or more chats.

  /set [name ...]  (Alias: /s)
    No arguments: show the active chat or group.
    One name: activate a chat or group.
    Group and member: activate the group and select its destination.
    Multiple chat names: create and activate a group.

  /list  (Alias: /l)
    List joined chats and groups.

  /del [name]  (Alias: /d)
    Leave a chat or delete a local group.
    No arguments: use the active chat or group.

  Participant counts use messages and presence from the last five minutes,
  including you. Messages shows the selected chat's count above two users;
  Info shows counts for the other group chats.
  Only the active destination announces presence, every 40-80 seconds.
  This announces channel participation, not your physical location.`},
	{"moderation", `  /filter [pattern|number]  (Alias: /f)
    With a pattern: add and enable an inclusion rule.
    With a number: toggle a rule. No arguments: list rules.
    Keep messages matching any enabled filter; mutes take priority.
    No enabled filters: allow all messages through the filter check.

  /unfilter [number]  (Alias: /uf)
    Remove a filter. No arguments: clear all filters.

  /mute [pattern|number]  (Alias: /m)
    With a pattern: add and enable an exclusion rule.
    With a number: toggle a rule. No arguments: list rules.
    Hide messages matching any enabled mute.

  /unmute [number]  (Alias: /um)
    Remove a mute. No arguments: clear all mutes.

  /block [@nick#key]  (Alias: /b)
    Block a participant. No arguments: list blocked participants.

  /unblock [number|@nick#key|pubkey]  (Alias: /ub)
    Unblock a participant. No arguments: list blocked participants.

  Patterns are case-sensitive text or /regexp/.
  Rules apply to new messages, not retained scrollback.`},
	{"appearance", `  /theme [name|number]  (Alias: /t)
    Select and save a theme. No arguments: list themes.

  /art [scene|random|off]  (Alias: /a)
    Choose a decorative ASCII scene. No arguments: list scenes.
    Saved across restarts; default: random scene at startup.
    Use random to reset the preference; select again for a new variation.
    Shown between Messages and Chats/Info in wide windows.

  /scrollbar [0|1|2]  (Alias: /sb)
    Select and save scrollbar placement: 0 off, 1 right, 2 left.
    Default: 1. No arguments: cycle 0 -> 1 -> 2 -> 0 silently.
    Repeat the current mode to show its options without saving.`},
	{"network", fmt.Sprintf(`  /relay [url ...|number]  (Alias: /r)
    With URLs: add explicit relays. With one number: remove a relay.
    No arguments: list configured relays.

  /pow [0..%d]  (Alias: /p)
    Set proof-of-work for the active chat or group.
    No arguments or 0: disable proof-of-work.`, maxPoW)},
	{"general", `  /nick [name]  (Alias: /n)
    Set a nickname. No arguments: use each chat's session nickname.

  /me <action>
    Describe your actions or emotions in the third person.
    Example: /me waves -> * nick#key waves.

  /clear  (Alias: /c)
    Clear messages, input history and cached nicknames.
    Keep Logs, participant counts, chats, groups, settings,
    identities and relay connections.

  /help [category]  (Alias: /h)
    Show one command category. No arguments: show all categories.

  /quit  (Alias: /q)
    Exit the application. Ctrl+D also exits.`},
}

func (c *client) getHelp(category string) {
	category = strings.ToLower(strings.TrimSpace(category))
	var names []string
	var text strings.Builder
	for _, section := range helpCategories {
		names = append(names, section.name)
		if category == section.name {
			c.emit(DisplayEvent{Type: "INFO", Content: strings.ToUpper(section.name) + ":\n" + section.commands})
			return
		}
		text.WriteString("\n\n" + strings.ToUpper(section.name) + ":\n" + section.commands)
	}
	categories := strings.Join(names, ", ")
	if category != "" {
		c.emit(DisplayEvent{Type: "INFO", Content: fmt.Sprintf("Unknown help category %q.\nCategories: %s.\nUse /help to show all commands.", category, categories)})
		return
	}
	// Scrollback follows the tail, so keep the category index visible at the end.
	c.emit(DisplayEvent{Type: "INFO", Content: "COMMANDS:" + text.String() + "\n\nCategories: " + categories + ".\nUse /help <category> to show one section."})
}
