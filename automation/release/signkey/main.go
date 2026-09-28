// Command signkey manages the Ed25519 key that signs wopr releases.
//
//	go run ./automation/release/signkey gen -out wopr-release.key
//	    Writes a new base64 private key to the file (mode 0600) and prints
//	    the base64 public key for internal/codingagent/release_key.go.
//	go run ./automation/release/signkey sign SHA256SUMS
//	    Signs the file with the base64 private key in WOPR_RELEASE_SIGNING_KEY
//	    and writes SHA256SUMS.sig (base64 signature).
//	go run ./automation/release/signkey verify [-pub KEY] SHA256SUMS
//	    Verifies SHA256SUMS.sig against KEY, by default the public key
//	    embedded in wopr, which is what `wopr update` checks.
//	go run ./automation/release/signkey pubkey
//	    Prints the public key of WOPR_RELEASE_SIGNING_KEY.
//
// The release workflow runs sign and then verify, so a release that the
// shipped binaries cannot verify is never published.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alexrudloff/wopr/internal/codingagent"
)

const keyEnv = "WOPR_RELEASE_SIGNING_KEY"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "signkey:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: signkey gen|sign|verify|pubkey")
	}
	switch args[0] {
	case "gen":
		fs := flag.NewFlagSet("gen", flag.ContinueOnError)
		out := fs.String("out", "", "file to write the base64 private key to (required)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *out == "" {
			return errors.New("gen: -out is required; the private key is never printed")
		}
		return gen(*out, stdout)
	case "sign":
		if len(args) != 2 {
			return errors.New("usage: signkey sign FILE")
		}
		key, err := privateKeyFromEnv()
		if err != nil {
			return err
		}
		return sign(key, args[1])
	case "verify":
		fs := flag.NewFlagSet("verify", flag.ContinueOnError)
		pub := fs.String("pub", codingagent.ReleaseSigningPublicKey, "base64 Ed25519 public key (default: the key embedded in wopr)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: signkey verify [-pub KEY] FILE")
		}
		return verify(*pub, fs.Arg(0), stdout)
	case "pubkey":
		key, err := privateKeyFromEnv()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)))
		return err
	default:
		return fmt.Errorf("unknown command %q; want gen, sign, verify, or pubkey", args[0])
	}
}

func gen(out string, stdout io.Writer) error {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("gen: %w (refusing to overwrite an existing key)", err)
	}
	if _, err := fmt.Fprintln(file, base64.StdEncoding.EncodeToString(private)); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "private key written to %s\npublic key: %s\n", out, base64.StdEncoding.EncodeToString(public))
	return err
}

// parsePrivateKey accepts a base64 64-byte private key or 32-byte seed.
func parsePrivateKey(encoded string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("%s is not base64: %w", keyEnv, err)
	}
	switch len(raw) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	default:
		return nil, fmt.Errorf("%s decodes to %d bytes; want %d (private key) or %d (seed)", keyEnv, len(raw), ed25519.PrivateKeySize, ed25519.SeedSize)
	}
}

func privateKeyFromEnv() (ed25519.PrivateKey, error) {
	encoded := os.Getenv(keyEnv)
	if strings.TrimSpace(encoded) == "" {
		return nil, fmt.Errorf("%s is not set", keyEnv)
	}
	return parsePrivateKey(encoded)
}

func sign(key ed25519.PrivateKey, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(key, data))
	return os.WriteFile(path+".sig", []byte(signature+"\n"), 0o644)
}

func verify(publicKey, path string, stdout io.Writer) error {
	if strings.TrimSpace(publicKey) == "" {
		return errors.New("verify: no public key; the key embedded in internal/codingagent/release_key.go is still the placeholder")
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKey))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("verify: public key must be base64 of %d bytes", ed25519.PublicKeySize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	encoded, err := os.ReadFile(path + ".sig")
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pub), data, signature) {
		return fmt.Errorf("verify: %s.sig is not a valid signature of %s for this key", path, path)
	}
	_, err = fmt.Fprintf(stdout, "%s.sig: OK\n", path)
	return err
}
