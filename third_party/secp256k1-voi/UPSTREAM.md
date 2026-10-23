# Local vendored fork of secp256k1-voi

Upstream: `gitlab.com/yawning/secp256k1-voi`
Pinned revision: `v0.0.0-20230925100816-f2616030848b`
Upstream license: BSD-3-Clause, see `LICENSE` (unchanged).

This directory is a verbatim copy of the pinned module plus the additions
listed below. It exists so the wNAF verification kernel is reviewable and
buildable without network access. The main module selects it with a `replace`
directive in `go.mod`; the version in `require` stays pinned to upstream.
Builds require a complete repository checkout, including this directory.
The relative replacement is not compatible with version-suffixed
`go install ...@version`; release binaries and builds from a checkout are the
supported distribution paths. Do not publish a module-only archive as if it
included this nested module.

## Additions (new files, no upstream code)

- `wnaf_table.go` — `WNAFPointTable` (odd multiples of P and beta*P),
  `Build`, signed-digit recoding, and `ScalarMultVartimeWNAF` /
  `DoubleScalarMultBasepointVartimeWNAF`. Public verification data only.
- `secec/bitcoin/schnorr_wnaf.go` — `AuthorVerifyTable`, an immutable table
  whose owner label is derived from the deserialized key inside the package,
  and `VerifyWithTable`, which applies the unchanged BIP-340 checks.
- `wnaf_table_test.go` — recoding boundary/round-trip and differential tests
  against the incumbent GLV multiply.
- `secec/bitcoin/schnorr_wnaf_test.go` — official BIP-340 vectors, signature
  mutations, key binding, zero-table/identity-forgery rejection and parity.

## Modified upstream file (one function body)

- `point_mul_glv.go`, `(*Point).DoubleScalarMultBasepointVartime` only: the
  variable-base multiply now builds a wNAF table in the stack frame and uses
  it instead of `scalarMultVartimeGLV`. Public API, ECDSA verification and all
  signing paths are unchanged. No upstream file was deleted.

`grep -rn "Local addition"` finds every local edit. Rebase procedure: copy the
new upstream revision, re-apply the additive files and the single function-body
change, then run `go test ./...` here and the main module's crypto tests.
