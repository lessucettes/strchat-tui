package client

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

var signerTestKeys = []string{
	strings.Repeat("0", 63) + "1",
	"B7E151628AED2A6ABF7158809CF4F3C762E7160F38B4DA56A784D9045190CFEF",
	"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364140",
	"0340034003400340034003400340034003400340034003400340034003400340",
}

func TestSessionSignerMatchesGoNostrSign(t *testing.T) {
	rng := &randReader{}
	for ki, key := range signerTestKeys {
		signer, err := newSessionSigner(key)
		if err != nil {
			t.Fatalf("key %d: %v", ki, err)
		}
		for i := range 12 {
			base := nostr.Event{
				Kind:      ephChatKind,
				CreatedAt: nostr.Timestamp(1700000000 + rng.intn(100000)),
				Content:   rng.text(0, 300),
				Tags:      nostr.Tags{{"d", "lobby"}, {"n", "alice"}},
			}
			if i%4 == 0 {
				base.Tags = nil // go-nostr normalizes nil tags to empty
			}
			if i%5 == 0 {
				base.Content = "héllo 世界\n\t" + base.Content
			}
			if i%7 == 0 {
				base.Tags = append(base.Tags, nostr.Tag{"p", strings.Repeat("a", 64)})
			}
			legacy := base
			if err := legacy.Sign(key); err != nil {
				t.Fatalf("legacy sign: %v", err)
			}
			modern := base
			if err := signer.sign(&modern); err != nil {
				t.Fatalf("session sign: %v", err)
			}
			if modern.PubKey != legacy.PubKey || modern.ID != legacy.ID || modern.Sig != legacy.Sig {
				t.Fatalf("key %d event %d: signer output differs from nostr.Event.Sign", ki, i)
			}
			if _, _, ok := verifyEventDigest(&modern, nil); !ok {
				t.Fatalf("key %d event %d: own signature did not verify", ki, i)
			}
		}
	}
}

func TestNewSessionSignerRejectsInvalidKeys(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"short":       strings.Repeat("0", 62) + "1",
		"long":        strings.Repeat("0", 66),
		"non-hex":     strings.Repeat("z", 64),
		"zero-scalar": strings.Repeat("0", 64),
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := newSessionSigner(key); err == nil {
				t.Fatal("accepted an invalid signing key")
			}
		})
	}
}

func TestChatSessionSigningIdentityValidation(t *testing.T) {
	key := signerTestKeys[0]
	signer, err := newSessionSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	// A session that predates the cached signer is validated lazily.
	if got, err := (chatSession{privKey: key, pubKey: signer.pubKey}).signingIdentity(); err != nil || got.pubKey != signer.pubKey {
		t.Fatalf("lazy signer: %v", err)
	}
	other, err := newSessionSigner(signerTestKeys[1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (chatSession{privKey: key, pubKey: other.pubKey}).signingIdentity(); err == nil {
		t.Fatal("accepted a key that does not match the session public key")
	}
	if _, err := (chatSession{}).signingIdentity(); err == nil {
		t.Fatal("accepted an empty signing key")
	}
}

func TestSessionSignerConcurrentSignAndVerify(t *testing.T) {
	signer, err := newSessionSigner(signerTestKeys[2])
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 8 {
				ev := nostr.Event{
					Kind:      nostr.KindClientAuthentication,
					CreatedAt: nostr.Timestamp(1700000000 + i),
					Tags:      nostr.Tags{{"relay", "ws://local"}, {"challenge", "c"}},
				}
				if err := signer.sign(&ev); err != nil {
					errs <- err
					return
				}
				if _, _, ok := verifyEventDigest(&ev, nil); !ok {
					errs <- errInvalidSignerKey
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestQueuedPublishKeepsCapturedSignerAfterIdentityRemoval(t *testing.T) {
	c, _ := testClient(t)
	key := signerTestKeys[1]
	signer, err := newSessionSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	c.chatKeys["lobby"] = chatSession{privKey: key, pubKey: signer.pubKey, nick: "alice", signer: signer}
	relay := &managedRelay{url: "local", connected: true, chats: []string{"lobby"}, publish: make(chan relayPublish, 1)}
	c.relays["local"] = relay
	c.publishMessage("hello")
	job := <-c.outgoing
	// Leaving the chat deletes the session; the queued job must still sign with
	// its captured immutable identity rather than a replacement or a zeroed key.
	delete(c.chatKeys, "lobby")
	go func() {
		request := <-relay.publish
		request.result <- nil
	}()
	result := executePublish(context.Background(), job)
	if result.accepted != 1 {
		t.Fatalf("captured identity publish failed: %s", result.message)
	}
	if _, _, ok := verifyEventDigest(&result.event, nil); !ok {
		t.Fatal("event signed by a retired identity did not verify")
	}
}

func TestRelayAuthReusesValidatedSigner(t *testing.T) {
	for _, key := range []string{
		signerTestKeys[0],
		"fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364142", // N+1
		strings.Repeat("f", 64),
	} {
		a := &relayAuth{}
		signer, err := newSessionSigner(key)
		if err != nil {
			t.Fatal(err)
		}
		first, err := a.signerFor(signer.pubKey, key)
		if err != nil {
			t.Fatal(err)
		}
		second, err := a.signerFor(signer.pubKey, key)
		if err != nil || first != second {
			t.Fatal("AUTH did not reuse the validated session signer")
		}
		if _, err := a.signerFor(signer.pubKey, signerTestKeys[1]); err == nil {
			t.Fatal("accepted an AUTH key that does not match its public key")
		}
		a.prune(map[string]string{})
		if len(a.signers) != 0 {
			t.Fatal("pruned AUTH signer cache still holds unused identities")
		}
	}
}
