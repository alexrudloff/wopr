package codingagent

// ReleaseSigningPublicKey is the base64-encoded Ed25519 public key that signs
// every release's SHA256SUMS (the SHA256SUMS.sig asset). `wopr update` refuses
// any release it cannot verify against this key.
//
// It is empty until the maintainer generates the release key pair; see
// docs/project/RELEASING.md ("One-time signing key setup"). While it is empty,
// self-update fails closed with "release signing key not configured".
const ReleaseSigningPublicKey = ""
