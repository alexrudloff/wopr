# Releasing WOPR

WOPR ships through [GitHub Releases](https://github.com/alexrudloff/wopr/releases)
and the Go module proxy. Pushing a `vMAJOR.MINOR.PATCH` tag runs
`.github/workflows/release.yml`, which publishes everything; nothing else hosts
a release.

## What a release contains

For `v0.3.0` the workflow publishes:

- `wopr-0.3.0-darwin-amd64.tar.gz`, `wopr-0.3.0-darwin-arm64.tar.gz`,
  `wopr-0.3.0-linux-amd64.tar.gz`, `wopr-0.3.0-linux-arm64.tar.gz`, and
  `wopr-0.3.0-windows-amd64.zip`. Each unpacks to `wopr-0.3.0-<os>-<arch>/`
  holding `wopr` (or `wopr.exe`), `LICENSE` and `NOTICE`;
- `SHA256SUMS`, the SHA-256 of every archive;
- `SHA256SUMS.sig`, a base64 Ed25519 signature of `SHA256SUMS`; and
- a GitHub build-provenance attestation for each archive
  (`wopr verify --provenance`, or `gh attestation verify`).

How each consumer checks it:

- `install.sh` (`curl -fsSL https://raw.githubusercontent.com/alexrudloff/wopr/main/install.sh | sh`)
  resolves the latest tag from the `releases/latest` redirect, checks the
  archive against `SHA256SUMS`, and installs `wopr` to `~/.local/bin`. It does
  not check the signature.
- `wopr update` and `/upgrade` resolve the latest tag the same way (no GitHub
  API quota), verify `SHA256SUMS.sig` against the public key compiled into the
  binary (`internal/codingagent/release_key.go`), check the archive's SHA-256,
  and only then replace the executable. `/upgrade` then offers to restart
  into the new version, reopening the session. The startup check uses the same
  redirect, names a newer release in a toast and the home screen footer, and
  is suppressed by `WOPR_OFFLINE=1`. The first start of a new version shows
  its `CHANGELOG.md` section once ("What's New"), so every release needs one.
- `go install github.com/alexrudloff/wopr/cmd/wopr@v0.3.0` builds from the
  module proxy.

## Cut a release

1. On `main`, set `const Version` in `coding/version/version.go` to the new
   version (for example `0.3.0`), add its `## [0.3.0] - <date>` section to
   `CHANGELOG.md`, commit, and merge. The version is a constant,
   not a linker flag, so `go install` builds report it too; the workflow fails
   when the tag and the constant differ.
2. Make sure `make check` passes on that commit.
3. Tag and push:

   ```bash
   git tag -a v0.3.0 -m "WOPR v0.3.0"
   git push origin v0.3.0
   ```

4. Watch the run: `gh run watch --repo alexrudloff/wopr`. It validates the tag,
   cross-builds the five archives with `CGO_ENABLED=0` and
   `-X main.Build=<commit>`, smoke-tests the linux/amd64 binary, writes and
   signs `SHA256SUMS`, verifies the signature against the key embedded in the
   source, attests provenance, and creates the release with generated notes. A
   tag with a pre-release suffix (`v0.3.0-rc.1`) becomes a GitHub pre-release,
   which `releases/latest`, `install.sh`, `wopr update`, and `/upgrade` skip.
5. Check the result from a clean machine:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/alexrudloff/wopr/main/install.sh | sh
   wopr --version
   GOBIN="$(mktemp -d)" GOMODCACHE="$(mktemp -d)" GOFLAGS= GOWORK=off \
     go install github.com/alexrudloff/wopr/cmd/wopr@v0.3.0
   ```

Never move or delete a published tag or replace a release's assets: the Go
checksum database records each module version permanently, and installed
binaries trust `SHA256SUMS` as signed. Fix a bad release with a new version.

## One-time signing key setup

The release workflow signs with the `WOPR_RELEASE_SIGNING_KEY` repository
secret, and `wopr update` verifies with the public key in
`internal/codingagent/release_key.go`. Until both exist, the workflow fails at
"Sign SHA256SUMS" and `wopr update` stops with
`release signing key not configured`.

1. Generate the key pair on a trusted machine, outside the repository:

   ```bash
   go run ./automation/release/signkey gen -out ~/wopr-release.key
   ```

   It writes the base64 private key to `~/wopr-release.key` (mode 0600, never
   overwritten) and prints `public key: <base64>`.

2. Store the private key as the repository secret:

   ```bash
   gh secret set WOPR_RELEASE_SIGNING_KEY --repo alexrudloff/wopr < ~/wopr-release.key
   ```

3. Paste the printed public key into `internal/codingagent/release_key.go`:

   ```go
   const ReleaseSigningPublicKey = "<base64 public key>"
   ```

   Commit it to `main` before tagging. Releases are verifiable only by
   binaries that carry this key, so the first signed release is the first one
   `wopr update` can install; earlier builds must reinstall once.

4. Move `~/wopr-release.key` to offline storage (a password manager or an
   encrypted backup) and delete the local copy. To recover the public key from
   it later: `WOPR_RELEASE_SIGNING_KEY="$(cat wopr-release.key)" go run ./automation/release/signkey pubkey`.

Check a downloaded release by hand with
`go run ./automation/release/signkey verify SHA256SUMS` (uses the embedded key;
pass `-pub <base64>` for another).

Rotating the key means shipping a release whose binary embeds the new public
key, signed with the old key, then switching the secret. Binaries older than
that release must reinstall with `install.sh`.
