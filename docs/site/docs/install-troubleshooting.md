# Install and troubleshoot WOPR

WOPR publishes release archives on [GitHub Releases](https://github.com/alexrudloff/wopr/releases). The archive names on this page are the ones the release workflow produces. To build `wopr` from source instead, follow the [Quickstart](quickstart.md).

## Verify what you installed

`wopr verify` prints the binary's identity and the SHA-256 of its bytes. Verification starts from that digest; the version string, file name, and download URL prove nothing on their own.

```bash
wopr verify                                   # this binary: identity and SHA-256
wopr verify --checksums SHA256SUMS wopr-<version>-linux-amd64.tar.gz
wopr verify --provenance ./wopr                # GitHub build provenance, via gh
```

`--checksums` requires each file's digest to match its entry in the release `SHA256SUMS`. `--provenance` runs `gh attestation verify`, which checks the Sigstore bundle, its transparency-log entry, and that the signer is WOPR's release workflow. A binary you built yourself has no attestation and reports as unverified.

## macOS

Browsers, AirDrop, and messaging apps add the `com.apple.quarantine` attribute to downloaded files. Gatekeeper then blocks an unsigned binary with a message that the developer cannot be verified. The release workflow does not sign binaries with an Apple Developer ID or notarize them yet; the release notes will state the current status. Files downloaded with `curl` or `wget` are not quarantined.

After you verify the SHA-256, remove the attribute from the binary or from the extracted archive directory:

```bash
xattr -l ./wopr
xattr -d com.apple.quarantine ./wopr
xattr -dr com.apple.quarantine ./wopr-<version>-darwin-arm64
```

Apple silicon Macs use the `darwin-arm64` archive and Intel Macs use `darwin-amd64`. `uname -m` prints `arm64` or `x86_64`.

## Windows

Files downloaded with a browser carry the Mark of the Web, and SmartScreen can warn that the app is unrecognized. After you verify the SHA-256, run `Unblock-File .\wopr.exe` in PowerShell. Microsoft Defender can flag unsigned Go binaries heuristically; verify the checksum and report a false positive at https://www.microsoft.com/wdsi/filesubmission.

Add the directory that contains `wopr.exe` to `PATH`, then open a new terminal. Windows Terminal gives the best keyboard and color support; see [Windows](windows.md).

## Linux

The release workflow builds binaries with cgo disabled, so they do not depend on the system C library. Use `linux-amd64` when `uname -m` prints `x86_64` and `linux-arm64` when it prints `aarch64`. If a download fails with `permission denied`, run `chmod +x wopr`; if the file sits on a `noexec` mount such as /tmp, move it to `~/.local/bin`.

## Proxies and certificates

WOPR's HTTP clients honor `HTTPS_PROXY`, `HTTP_PROXY`, and `NO_PROXY`. On macOS and Windows WOPR trusts the system certificate store. On Linux, set `SSL_CERT_FILE` or `SSL_CERT_DIR` when a corporate proxy re-signs TLS traffic.

## Terminal display

For tmux settings, see [tmux](tmux.md).

## Report a problem

Include the output of `wopr verify`, your operating system and terminal, and the steps that reproduce the problem.
