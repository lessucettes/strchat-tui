package client

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func TestCompletionMatchesFullSort(t *testing.T) {
	c, events := testClient(t)
	c.config.Views = []View{{Name: "group", IsGroup: true, Children: []string{"lobby", "other"}}}
	c.config.ActiveViewName = "group"
	rng := rand.New(rand.NewPCG(13, 29))
	for round := range 50 {
		c.userContext.Purge()
		for i := range 40 + rng.IntN(100) {
			key := fmt.Sprintf("%064x", i)
			c.userContext.Add(key, userContext{
				nick:        []string{"alice", "alice#abc", "bob", "ali ce", "Я", "@alice"}[rng.IntN(6)],
				shortPubKey: fmt.Sprintf("%04x", rng.IntN(4)),
				chat:        []string{"lobby", "other", "outside"}[rng.IntN(3)],
			})
		}
		for _, prefix := range []string{"", "@ali", "@alice#", "@Я", "@none"} {
			var want []string
			byIdentifier := make(map[string][]string)
			for _, key := range c.userContext.Keys() {
				value, _ := c.userContext.Peek(key)
				if value.chat == "outside" || !strings.HasPrefix(value.nick, strings.TrimPrefix(prefix, "@")) {
					continue
				}
				identifier := "@" + value.nick + "#" + value.shortPubKey + " "
				byIdentifier[identifier] = append(byIdentifier[identifier], "@"+value.nick+"#"+key+" ")
			}
			for identifier, full := range byIdentifier {
				if len(full) == 1 {
					want = append(want, identifier)
				} else {
					want = append(want, full...)
				}
			}
			slices.Sort(want)
			want = want[:min(10, len(want))]
			c.handleNickCompletion(prefix)
			got := (<-events).Payload.([]string)
			if !slices.Equal(got, want) {
				t.Fatalf("round %d prefix %q: got %q, want %q", round, prefix, got, want)
			}
		}
	}
}
