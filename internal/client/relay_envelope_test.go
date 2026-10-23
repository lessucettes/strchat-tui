package client

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

// oldRelayEnvelope is the literal pre-spike production parse: a generic outer
// json.Unmarshal into []json.RawMessage plus label/id decode.
func oldRelayEnvelope(data []byte) (frame []json.RawMessage, label, id string, ok bool) {
	if json.Unmarshal(data, &frame) != nil || len(frame) < 2 {
		return nil, "", "", false
	}
	if json.Unmarshal(frame[0], &label) != nil || json.Unmarshal(frame[1], &id) != nil {
		return nil, "", "", false
	}
	return frame, label, id, true
}

// newRelayEnvelope is the candidate parse.
func newRelayEnvelope(data []byte) (parts [relayEnvelopeParts][]byte, count int, label, id string, ok bool) {
	parts, count = splitRelayEnvelope(data)
	if count < 2 {
		return parts, count, "", "", false
	}
	label, id, ok = decodeRelayEnvelopeHead(parts[0], parts[1])
	return parts, count, label, id, ok
}

// envelopeGate mirrors the per-kind length gates in serveRelay. It returns
// whether the kind is handled and whether its operand payload is present.
// Keep this oracle in sync with serveRelayWithPing's AUTH/EVENT/EOSE/CLOSED/OK
// cases; each handled arity must remain below relayEnvelopeParts.
func envelopeGate(label string, count int) (handled, accepted bool) {
	switch label {
	case "AUTH":
		return true, count == 2
	case "EVENT", "EOSE", "CLOSED":
		return true, count == 3
	case "OK":
		return true, count == 4
	default:
		return false, false
	}
}

// compareEnvelope asserts the candidate matches the old parser for one frame,
// including accepted/rejected status, label, id, capped length, element bytes,
// and per-kind length-gate decisions.
func compareEnvelope(t *testing.T, data []byte) {
	t.Helper()
	oldFrame, oldLabel, oldID, oldOK := oldRelayEnvelope(data)
	original := bytes.Clone(data)
	newParts, newCount, newLabel, newID, newOK := newRelayEnvelope(data)
	if !bytes.Equal(data, original) {
		t.Fatal("envelope parsing mutated the owned frame")
	}
	if oldOK != newOK {
		t.Fatalf("frame %.200q: accept mismatch old=%v new=%v", data, oldOK, newOK)
	}
	if !oldOK {
		return
	}
	if oldLabel != newLabel || oldID != newID {
		t.Fatalf("frame %.200q: label/id mismatch old=(%q,%q) new=(%q,%q)", data, oldLabel, oldID, newLabel, newID)
	}
	oldCount := min(len(oldFrame), relayEnvelopeParts)
	if oldCount != newCount {
		t.Fatalf("frame %.200q: count mismatch old=%d new=%d", data, oldCount, newCount)
	}
	offset := 0
	for i := 0; i < oldCount; i++ {
		if !bytes.Equal(bytes.TrimSpace(oldFrame[i]), newParts[i]) {
			t.Fatalf("frame %.200q: element %d mismatch old=%.100q new=%.100q", data, i, oldFrame[i], newParts[i])
		}
		start := bytes.Index(data[offset:], newParts[i])
		if start < 0 || len(newParts[i]) == 0 || &newParts[i][0] != &data[offset+start] {
			t.Fatalf("element %d does not alias its input span", i)
		}
		offset += start + len(newParts[i])
	}
	oldHandled, oldAccepted := envelopeGate(oldLabel, len(oldFrame))
	newHandled, newAccepted := envelopeGate(newLabel, newCount)
	if oldHandled != newHandled || oldAccepted != newAccepted {
		t.Fatalf("frame %.200q: gate mismatch old=(%v,%v) new=(%v,%v)", data, oldHandled, oldAccepted, newHandled, newAccepted)
	}
}

func TestEnvelopeSplitMatchesRawMessage(t *testing.T) {
	event := &nostr.Event{
		Kind: ephChatKind, CreatedAt: nostr.Now(),
		Tags:    nostr.Tags{{"d", "lobby"}, {"n", "peer"}, {"t", "two"}},
		Content: strings.Repeat("ab \\\"quote\\\" \\u00e9 and more ", 40),
	}
	if err := event.Sign(transportTestKey); err != nil {
		t.Fatal(err)
	}
	signed, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	frames := [][]byte{
		[]byte(`["AUTH","challenge"]`),
		[]byte(` ["AUTH" , "challenge" ] `),
		[]byte(`["AUTH","challenge","extra"]`),
		[]byte(`["EVENT","chat",` + string(signed) + `]`),
		[]byte(`["EVENT","chat"]`),
		[]byte(`["EVENT","chat",` + string(signed) + `,"extra"]`),
		[]byte(`["EVENT","other",` + string(signed) + `]`),
		[]byte(`["EVENT",` + string(signed) + `,"chat"]`),
		[]byte(`["EOSE","chat"]`),
		[]byte(`["EOSE","chat",["auth"]]`),
		[]byte(`["EOSE","chat",["other"],1]`),
		[]byte(`["CLOSED","chat","auth-required: go"]`),
		[]byte(`["CLOSED","chat","restricted: no"]`),
		[]byte(`["CLOSED","chat",""]`),
		[]byte(`["OK","id",true,""]`),
		[]byte(`["OK","id",false,"blocked: nope"]`),
		[]byte(`["OK","id",true]`),
		[]byte(`["OK","id",true,"",1]`),
		[]byte(`["NOTICE","hello"]`),
		[]byte(`["NOTICE"]`),
		[]byte(`[]`),
		[]byte(`[null]`),
		[]byte(`[null,null]`),
		[]byte(`[null, 1]`),
		[]byte(`[1,2]`),
		[]byte(`[true,false]`),
		[]byte(`[{"a":[1,{"b":"c"}]}, "chat", [1,[2,[3]]]]`),
		[]byte(`["\u0045VENT","chat",{"id":"x"}]`),
		[]byte(`["EVENT","chat",{"id":"x","content":null,"tags":null}]`),
		[]byte(`["EVENT","chat",{"id":1,"created_at":"bad","kind":1.5,"tags":{}}]`),
		[]byte(`["EVENT","chat",{"unknown":"ignored","content":"kept"}]`),
		[]byte("\xff\xfe"),
		[]byte("[\"EVENT\",\"chat\",{\"content\":\"a\xffb\"}]"),
		[]byte("[\"EVENT\",\"chat\",{\"a\xffb\":1}]"),
		[]byte("[\"EVENT\",\"chat\",\"\\ud800\"]"),
		[]byte(`["EVENT","chat",{"content":"a\nb"}]`),
		[]byte(`["EVENT","chat",{"content":"a	b"}]`),
		[]byte(`["EVENT","chat",]`),
		[]byte(`[,1]`),
		[]byte(`["EVENT" "chat"]`),
		[]byte(`["EVENT","chat"`),
		[]byte(`["EVENT","chat"] tail`),
		[]byte(`null`),
		[]byte(`{}`),
		[]byte(`"EVENT"`),
		[]byte(`123`),
		[]byte(``),
		[]byte(` `),
		[]byte("\xef\xbb\xbf[\"EVENT\"]"),
		[]byte(`["E","chat",{"a":1,"a":2}]`),
		[]byte(`["EVENT","chat",{"content":"` + strings.Repeat("x", 4096) + `"}]`),
		[]byte(`["EVENT","chat",{"content":"quote \" backslash \\ slash \\\" end"}]`),
		[]byte(`["EVENT","chat",{"content":"\u0000\u001f\u007f"}]`),
		[]byte(`[1,2,3,4,5,6,7,8,9,10]`),
		[]byte(`["OK","id",true,"",1,2,3,4,5]`),
	}
	frames = append(frames,
		[]byte(`["EVENT","chat",{"content":"`+strings.Repeat(`\\`, 30000)+`"}]`),
		[]byte(`["EVENT","chat",{"tags":[`+strings.TrimSuffix(strings.Repeat(`["x"],`, 8000), ",")+`]}]`),
		[]byte(`["EVENT","\u0063hat",{}]`),
	)
	deep := []byte("[" + strings.Repeat("[", 12000) + "1" + strings.Repeat("]", 12000) + "]")
	frames = append(frames, deep)
	for _, depth := range []int{9998, 9999, 10000, 10001} {
		frames = append(frames, []byte(strings.Repeat("[", depth)+"1"+strings.Repeat("]", depth)))
	}
	for _, data := range frames {
		compareEnvelope(t, data)
	}
}

func TestEnvelopeSplitRandomized(t *testing.T) {
	base := [][]byte{
		[]byte(`["AUTH","challenge"]`),
		[]byte(`["EVENT","chat",{"id":"abc","content":"hello\u00e9","tags":[["d","lobby"]]}]`),
		[]byte(`["OK","id",true,""]`),
		[]byte(`["CLOSED","chat","auth-required: go"]`),
		[]byte(`["EOSE","chat",["auth"]]`),
		[]byte(`["NOTICE","hello",{"nested":[1,2,{"x":"y"}]}]`),
	}
	rng := rand.New(rand.NewPCG(0x5eed, 0xbeef))
	alphabet := []byte(`[]{}",:0123456789.eE+-truefalsn \t\n\r\uABCDEFxyz` + "\xff")
	for i := 0; i < 100000; i++ {
		var data []byte
		if i%3 == 0 {
			data = make([]byte, 1+rng.IntN(24))
			for j := range data {
				data[j] = alphabet[rng.IntN(len(alphabet))]
			}
		} else {
			data = append([]byte(nil), base[rng.IntN(len(base))]...)
			for edits := 1 + rng.IntN(3); edits > 0; edits-- {
				pos := rng.IntN(len(data) + 1)
				switch rng.IntN(3) {
				case 0:
					data = append(data[:pos], append([]byte{alphabet[rng.IntN(len(alphabet))]}, data[pos:]...)...)
				case 1:
					if pos < len(data) {
						data = append(data[:pos], data[pos+1:]...)
					}
				case 2:
					if pos < len(data) {
						data[pos] = alphabet[rng.IntN(len(alphabet))]
					}
				}
			}
		}
		compareEnvelope(t, data)
	}
}

func FuzzEnvelopeSplitMatchesRawMessage(f *testing.F) {
	seeds := []string{
		`["AUTH","c"]`, `["EVENT","chat",{"id":"x"}]`, `["OK","id",true,""]`,
		`["EOSE","chat",["auth"]]`, `["CLOSED","chat","x"]`, `[]`, `null`, `"x"`, `[1,]`,
		`["\u0045VENT","chat",{}]`, `["EVENT","\u0063hat",{}]`,
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		compareEnvelope(t, data)
	})
}

func BenchmarkEnvelopeWas(b *testing.B) {
	frames := pipelineFixtures(b)
	for _, size := range []string{"short", "long"} {
		data := frames[size][0]
		b.Run(size, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, _, _, ok := oldRelayEnvelope(data); !ok {
					b.Fatal("rejected")
				}
			}
		})
	}
}

func BenchmarkEnvelopeSplit(b *testing.B) {
	frames := pipelineFixtures(b)
	for _, size := range []string{"short", "long"} {
		data := frames[size][0]
		b.Run(size, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, _, _, _, ok := newRelayEnvelope(data); !ok {
					b.Fatal("rejected")
				}
			}
		})
	}
}

func BenchmarkEnvelopeSplitValidOnly(b *testing.B) {
	frames := pipelineFixtures(b)
	for _, size := range []string{"short", "long"} {
		data := frames[size][0]
		b.Run(size, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, n := splitRelayEnvelope(data); n != 3 {
					b.Fatal("count", n)
				}
			}
		})
	}
}

// Exercise valid untrusted frames close to the production wire limit, not
// accepted-event throughput. Payloads may be rejected later by event validation.
func BenchmarkEnvelopeAdversarial(b *testing.B) {
	frames := []struct{ name, text string }{
		{"denseEscapes", `["EVENT","chat",{"content":"` + strings.Repeat(`\\`, 30000) + `"}]`},
		{"manyStrings", `["EVENT","chat",{"tags":[` + strings.TrimSuffix(strings.Repeat(`["x"],`, 8000), ",") + `]}]`},
	}
	for _, fixture := range frames {
		data := []byte(fixture.text)
		if len(data) > 64*1024 || !json.Valid(data) {
			b.Fatalf("bad bounded fixture %s", fixture.name)
		}
		b.Run(fixture.name, func(b *testing.B) {
			b.Run("old", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, _, _, ok := oldRelayEnvelope(data); !ok {
						b.Fatal("rejected")
					}
				}
			})
			b.Run("candidate", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, _, _, _, ok := newRelayEnvelope(data); !ok {
						b.Fatal("rejected")
					}
				}
			})
		})
	}
}
