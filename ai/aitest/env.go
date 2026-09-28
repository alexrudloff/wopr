package aitest

import "path/filepath"

// HermeticCredentialEnv returns variables that stop SDK credential chains from
// reading the developer's credential files or instance metadata, pointing
// them into dir instead. Test setup applies them after clearing
// ProviderCredentialEnvVars.
func HermeticCredentialEnv(dir string) map[string]string {
	return map[string]string{
		"AWS_CONFIG_FILE":             filepath.Join(dir, "aws-config"),
		"AWS_SHARED_CREDENTIALS_FILE": filepath.Join(dir, "aws-credentials"),
		"AWS_EC2_METADATA_DISABLED":   "true",
		"CLOUDSDK_CONFIG":             filepath.Join(dir, "gcloud"),
	}
}
