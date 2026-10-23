package client

import (
	"bytes"
	"encoding/json"
)

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
