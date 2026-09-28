package codingagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SelfUpdateTier selects one owner before mutation.
type SelfUpdateTier string

const (
	// TierStandalone is a writable standalone binary that wopr replaces in
	// place from a verified GitHub release.
	TierStandalone SelfUpdateTier = "standalone"
	// TierImmutableBinary is a deployment-declared immutable release. Its owning
	// release is re-pulled; wopr does not replace it in place.
	TierImmutableBinary SelfUpdateTier = "immutable-binary"
	// TierContainer is an OCI image deployment. The
	// authenticated pull/redeploy path is reported; wopr does not rewrite a
	// running image.
	TierContainer SelfUpdateTier = "container"
	// TierUnsupported covers read-only and Windows installations. wopr refuses
	// mutation and reports the executable path plus concrete remediation.
	TierUnsupported SelfUpdateTier = "unsupported"
)

// SelfUpdateProvenance is the resolved ownership of the running executable.
type SelfUpdateProvenance struct {
	Tier    SelfUpdateTier
	ExePath string
}

// TierError reports that tier resolution refused the installation. It carries
// a concrete, non-looping remediation so the caller surfaces it verbatim.
type TierError struct {
	Remediation string
}

func (e *TierError) Error() string { return e.Remediation }

// ResolveSelfUpdateTier classifies the running executable before any
// mutation. It never mutates state.
//
// Resolution order: explicit deployment override (WOPR_INSTALL_TIER), then
// Windows (never replaced in place), then a writable standalone binary.
func ResolveSelfUpdateTier() (*SelfUpdateProvenance, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate wopr executable: %w", err)
	}
	return resolveSelfUpdateTierOn(runtime.GOOS, exe)
}

// resolveSelfUpdateTierOn classifies exe as an installation on goos.
func resolveSelfUpdateTierOn(goos, exe string) (*SelfUpdateProvenance, error) {
	// Follow symlinks to the real target so writability is evaluated against
	// the binary wopr would actually replace.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	// WOPR_INSTALL_TIER lets a deployment declare its provenance when
	// filesystem detection is unreliable inside a container or image. Only
	// non-mutating tiers are accepted this way.
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WOPR_INSTALL_TIER"))) {
	case "container", "image":
		return &SelfUpdateProvenance{Tier: TierContainer, ExePath: exe}, nil
	case "immutable-binary":
		return &SelfUpdateProvenance{Tier: TierImmutableBinary, ExePath: exe}, nil
	case "":
		// fall through to filesystem detection
	default:
		return nil, &TierError{
			Remediation: fmt.Sprintf("unknown WOPR_INSTALL_TIER value %q; unset it or set it to one of: container, image, immutable-binary", os.Getenv("WOPR_INSTALL_TIER")),
		}
	}

	if goos == "windows" {
		// A standalone wopr.exe is not replaced in place.
		return &SelfUpdateProvenance{Tier: TierUnsupported, ExePath: exe}, nil
	}
	if isWritableReplacement(exe) {
		return &SelfUpdateProvenance{Tier: TierStandalone, ExePath: exe}, nil
	}
	return &SelfUpdateProvenance{Tier: TierUnsupported, ExePath: exe}, nil
}

func isWritableReplacement(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return replacementDirectoryWritable(path)
}

// ImmutableBinaryRemediation is the exact, non-looping instruction for an
// immutable release. wopr does not replace it in place; the owning release is
// re-pulled.
func ImmutableBinaryRemediation(exePath string) string {
	return fmt.Sprintf(
		"%s is an immutable release. It cannot be updated in place. "+
			"Pull the new release artifact from your provider, then replace %s.",
		AppName, exePath)
}

// ContainerRemediation is the exact authenticated pull/redeploy instruction for
// an OCI image deployment. wopr performs no
// transport and knows no deployment topology, so a deployment that owns a specific
// redeploy operation supplies it verbatim through WOPR_REDEPLOY_INSTRUCTION.
// wopr still owns the surrounding facts (artifact identity, executable, and the
// guarantee that the running image is never rewritten); only the operation comes
// from the deployment. Without that metadata wopr falls back to the generic image
// pull, which is correct for a plain OCI/container install but not for an
// orchestrator-managed deployment.
func ContainerRemediation(exePath string) string {
	ref := strings.TrimSpace(os.Getenv("WOPR_IMAGE_REF"))
	if instruction := strings.TrimSpace(os.Getenv("WOPR_REDEPLOY_INSTRUCTION")); instruction != "" {
		if ref != "" {
			return fmt.Sprintf(
				"%s is running from image %s. %s The running image is not rewritten. Executable: %s.",
				AppName, ref, instruction, exePath)
		}
		return fmt.Sprintf(
			"%s is running inside a container or deployment image. %s The running image is not rewritten. Executable: %s.",
			AppName, instruction, exePath)
	}
	if ref != "" {
		return fmt.Sprintf(
			"%s is running from image %s. Pull the new image (authenticate to your registry first: `%s pull %s`) and re-deploy. The running image is not rewritten. Executable: %s.",
			AppName, ref, imagePullCommand(), ref, exePath)
	}
	return fmt.Sprintf(
		"%s is running inside a container or deployment image. Pull the new image (authenticate to your registry first) and re-deploy. The running image is not rewritten. Executable: %s. "+
			"Set WOPR_IMAGE_REF to emit the exact image reference and pull command.",
		AppName, exePath)
}

// UnsupportedRemediation is the concrete reinstall/download instruction for a
// read-only, Windows, or unknown-provenance installation. It never repeats the
// failed self-update command.
func UnsupportedRemediation(exePath string) string {
	return fmt.Sprintf("%s cannot replace this installation in place. Executable: %s. %s",
		AppName, exePath, SelfUpdateFallback())
}

func imagePullCommand() string {
	if c := strings.TrimSpace(os.Getenv("WOPR_IMAGE_PULL_CMD")); c != "" {
		return c
	}
	return "docker"
}

// SelfUpdateActionResult is the outcome of attempting one tier's update. The
// caller surfaces Action and Message verbatim; Done marks a successful
// mutation. Cause carries the originating error so the caller can distinguish a
// benign "up to date" from a real failure.
type SelfUpdateActionResult struct {
	Done    bool
	Action  string
	Message string
	Cause   error
}

// ApplySelfUpdateTier resolves exactly one installation tier and applies its
// update, surfacing any failure from the started tier without falling through.
// The standalone download/replace path is supplied via applyStandalone so the
// cmd layer keeps ownership of the HTTP client, release, and version compare.
func ApplySelfUpdateTier(applyStandalone func(exePath string) error) SelfUpdateActionResult {
	prov, err := ResolveSelfUpdateTier()
	if err != nil {
		if te, ok := errors.AsType[*TierError](err); ok {
			return SelfUpdateActionResult{Action: "refused", Message: te.Error(), Cause: te}
		}
		return SelfUpdateActionResult{Action: "error", Message: err.Error(), Cause: err}
	}
	return applyProvenance(prov, applyStandalone)
}

// applyProvenance applies one resolved tier. Once the standalone tier starts,
// its failure surfaces from that tier and never falls through to another.
func applyProvenance(prov *SelfUpdateProvenance, applyStandalone func(exePath string) error) SelfUpdateActionResult {
	switch prov.Tier {
	case TierStandalone:
		if err := applyStandalone(prov.ExePath); err != nil {
			return SelfUpdateActionResult{Action: "standalone-failed", Message: err.Error(), Cause: err}
		}
		return SelfUpdateActionResult{Done: true, Action: "standalone-updated"}
	case TierImmutableBinary:
		return SelfUpdateActionResult{Action: "refused", Message: ImmutableBinaryRemediation(prov.ExePath)}
	case TierContainer:
		return SelfUpdateActionResult{Action: "refused", Message: ContainerRemediation(prov.ExePath)}
	default:
		return SelfUpdateActionResult{Action: "refused", Message: UnsupportedRemediation(prov.ExePath)}
	}
}
