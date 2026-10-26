package client

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"fiatjaf.com/nostr"
)

// decodeRelayEvent keeps wire validation at the boundary before converting to
// binary identifiers. The library's Event decoder silently ignores malformed
// hex and truncates kinds to uint16; using it here would lose admission checks.
// JSON input is bounded by maxFrameBytes in the transport.
func decodeRelayEvent(data []byte) (*nostr.Event, error) {
	var wire struct {
		ID        string          `json:"id"`
		PubKey    string          `json:"pubkey"`
		CreatedAt nostr.Timestamp `json:"created_at"`
		Kind      int64           `json:"kind"`
		Tags      nostr.Tags      `json:"tags"`
		Content   string          `json:"content"`
		Sig       string          `json:"sig"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}
	if len(wire.ID) != 64 || len(wire.PubKey) != 64 || len(wire.Sig) != 128 || wire.Kind < 0 || wire.Kind > 65535 ||
		strings.Trim(wire.ID, "0123456789abcdef") != "" || strings.Trim(wire.PubKey, "0123456789abcdef") != "" {
		return nil, errors.New("invalid event identifiers or kind")
	}
	event := &nostr.Event{Kind: nostr.Kind(wire.Kind), CreatedAt: wire.CreatedAt, Tags: wire.Tags, Content: wire.Content}
	if _, err := hex.Decode(event.ID[:], []byte(wire.ID)); err != nil {
		return nil, err
	}
	if _, err := hex.Decode(event.PubKey[:], []byte(wire.PubKey)); err != nil {
		return nil, err
	}
	if _, err := hex.Decode(event.Sig[:], []byte(wire.Sig)); err != nil {
		return nil, err
	}
	return event, nil
}

// relayEnvelopeParts bounds the top-level values retained from one relay frame.
// A relay frame that carries more values than this is reported with this count
// as a sentinel ("this many or more").
const relayEnvelopeParts = 5

// splitRelayEnvelope validates data as a single JSON value and records up to
// five top-level array elements without copying. The returned count is capped
// at relayEnvelopeParts, so a count of 5 means "five or more". serveRelay
// requires fewer than five values for every message kind it handles (AUTH 2,
// EVENT/EOSE/CLOSED 3, OK 4), so the capped count preserves each exact-length
// rejection and the generic "at least 2" pre-check while never materializing
// []json.RawMessage.
//
// The scanner runs only after json.Valid accepted the whole frame. It
// therefore walks known-valid JSON and only locates top-level commas and value
// boundaries; it is not a second JSON validator. Element slices alias data.
func splitRelayEnvelope(data []byte) ([relayEnvelopeParts][]byte, int) {
	var parts [relayEnvelopeParts][]byte
	if !json.Valid(data) {
		return parts, 0
	}
	i := skipRelayJSONSpace(data, 0)
	if i == len(data) || data[i] != '[' {
		return parts, 0
	}
	i = skipRelayJSONSpace(data, i+1)
	if i == len(data) || data[i] == ']' {
		return parts, 0
	}
	n := 0
	for {
		if n == relayEnvelopeParts || i >= len(data) {
			return parts, n
		}
		start := i
		i = relayJSONValueEnd(data, i)
		end := i
		for end > start && relayJSONSpace(data[end-1]) {
			end--
		}
		parts[n] = data[start:end]
		n++
		i = skipRelayJSONSpace(data, i)
		if i == len(data) || data[i] != ',' {
			return parts, n
		}
		i = skipRelayJSONSpace(data, i+1)
	}
}

// decodeRelayEnvelopeHead avoids generic string decoding for ordinary data
// traffic. Escaped spellings and every other type retain stdlib semantics.
func decodeRelayEnvelopeHead(kind, subscription []byte) (string, string, bool) {
	if string(kind) == `"EVENT"` && string(subscription) == `"chat"` {
		return "EVENT", "chat", true
	}
	var label, id string
	if json.Unmarshal(kind, &label) != nil || json.Unmarshal(subscription, &id) != nil {
		return "", "", false
	}
	return label, id, true
}

// relayJSONValueEnd returns the index just past the value starting at i. The
// caller guarantees data is valid JSON beginning at i.
func relayJSONValueEnd(data []byte, i int) int {
	switch data[i] {
	case '"':
		return relayJSONStringEnd(data, i)
	case '{', '[':
		depth := 0
		for i < len(data) {
			switch data[i] {
			case '"':
				i = relayJSONStringEnd(data, i)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
			i++
		}
		return i
	default:
		for i < len(data) && data[i] != ',' && data[i] != ']' {
			i++
		}
		return i
	}
}

// relayJSONStringEnd returns the index just past the string starting at i.
// Forward searches consume disjoint spans. Count only the backslash run just
// before each quote, keeping work linear for escape-dense and many-string data.
func relayJSONStringEnd(data []byte, i int) int {
	i++
	for i < len(data) {
		quote := bytes.IndexByte(data[i:], '"')
		if quote < 0 {
			return len(data)
		}
		end := i + quote
		start := end
		for start > i && data[start-1] == '\\' {
			start--
		}
		if (end-start)&1 == 0 {
			return end + 1
		}
		i = end + 1
	}
	return i
}

func skipRelayJSONSpace(data []byte, i int) int {
	for i < len(data) && relayJSONSpace(data[i]) {
		i++
	}
	return i
}

func relayJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
