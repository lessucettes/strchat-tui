module github.com/lessucettes/strchat-tui

go 1.27.0

// Pinned upstream plus the public-verification wNAF kernel; see UPSTREAM.md.
replace gitlab.com/yawning/secp256k1-voi => ./third_party/secp256k1-voi

require (
	github.com/btcsuite/btcd/btcec/v2 v2.5.0
	github.com/coder/websocket v1.8.15
	github.com/gdamore/tcell/v2 v2.13.10
	github.com/gorilla/websocket v1.5.3
	github.com/hashicorp/golang-lru/v2 v2.0.7
	github.com/magefile/mage v1.17.2
	github.com/mmcloughlin/geohash v0.10.0
	github.com/nbd-wtf/go-nostr v0.52.3
	github.com/rivo/tview v0.42.0
	github.com/rivo/uniseg v0.4.7
	gitlab.com/yawning/secp256k1-voi v0.0.0-20230925100816-f2616030848b
)

require (
	github.com/ImVexed/fasturl v0.0.0-20230304231329-4e41488060f3 // indirect
	github.com/btcsuite/btcd/chainhash/v2 v2.0.0 // indirect
	github.com/bytedance/gopkg v0.1.4 // indirect
	github.com/bytedance/sonic v1.15.4 // indirect
	github.com/bytedance/sonic/loader v0.5.3 // indirect
	github.com/cloudwego/base64x v0.1.7 // indirect
	github.com/decred/dcrd/crypto/blake256 v1.1.0 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1 // indirect
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.4.1 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mailru/easyjson v0.9.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/puzpuzpuz/xsync/v3 v3.5.1 // indirect
	github.com/tidwall/gjson v1.20.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.2 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	gitlab.com/yawning/tuplehash v0.0.0-20230713102510-df83abbf9a02 // indirect
	golang.org/x/arch v0.32.0 // indirect
	golang.org/x/crypto v0.36.0 // indirect
	golang.org/x/exp v0.0.0-20261014143641-f0251eb4aa3e // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/term v0.47.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)
