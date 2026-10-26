package client

import (
	"bytes"
	"crypto/sha256"
	"encoding"
	"encoding/json"
	"errors"
	"hash"
	"strconv"

	"fiatjaf.com/nostr"
)

// nonceTemplate identifies an editable decimal slot in the authoritative
// canonical serialization. It belongs to one job and contains no signing key.
type nonceTemplate struct {
	canonical  []byte
	start, end int
}

func newNonceTemplate(event *nostr.Event) (*nonceTemplate, error) {
	last := len(event.Tags) - 1
	if last < 0 || len(event.Tags[last]) != 3 || event.Tags[last][0] != "nonce" || event.Tags[last][1] != "0" {
		return nil, errors.New("mining requires a final zero nonce tag")
	}
	canonical := serializeCanonicalEvent(event)
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	// Canonical NIP-01 array: [0, pubkey, created_at, kind, tags, content].
	// Locate the tag structurally, never by matching a user-controlled marker.
	var ignored json.RawMessage
	for range 4 {
		if err := decoder.Decode(&ignored); err != nil {
			return nil, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	for i, tag := range event.Tags {
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		for j := range tag {
			if err := decoder.Decode(&ignored); err != nil {
				return nil, err
			}
			if i == last && j == 1 {
				if !bytes.Equal(ignored, []byte(`"0"`)) {
					return nil, errors.New("invalid canonical nonce slot")
				}
				end := int(decoder.InputOffset()) - 1 // closing quote stays in suffix
				return &nonceTemplate{canonical: canonical, start: end - 1, end: end}, nil
			}
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("missing canonical nonce slot")
}

type miningHasher struct {
	hash        hash.Hash
	restore     encoding.BinaryUnmarshaler
	prefixState []byte
	suffix      []byte
	digits      [20]byte
	output      [32]byte
}

func newMiningHasher(event *nostr.Event) (*miningHasher, error) {
	template, err := newNonceTemplate(event)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write(template.canonical[:template.start])
	state, err := h.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return nil, err
	}
	return &miningHasher{hash: h, restore: h.(encoding.BinaryUnmarshaler),
		prefixState: state, suffix: template.canonical[template.end:]}, nil
}

func (h *miningHasher) digest(nonce uint64) ([32]byte, error) {
	if err := h.restore.UnmarshalBinary(h.prefixState); err != nil {
		return [32]byte{}, err
	}
	h.hash.Write(strconv.AppendUint(h.digits[:0], nonce, 10))
	h.hash.Write(h.suffix)
	h.hash.Sum(h.output[:0])
	return h.output, nil
}
