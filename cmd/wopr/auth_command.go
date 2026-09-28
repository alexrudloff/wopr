package main

// Parsing, usage, and argument validation for `wopr auth check`, `wopr auth
// print-api-key`, and `wopr auth print-bearer-token`.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent"
)

// AuthCommandKind identifies an auth subcommand.
type AuthCommandKind string

const (
	AuthCommandCheck       AuthCommandKind = "check"
	AuthCommandAPIKey      AuthCommandKind = "api_key"
	AuthCommandBearerToken AuthCommandKind = "bearer_token"
)

// AuthCommand is a parsed auth subcommand.
type AuthCommand struct {
	Kind        AuthCommandKind
	Args        []string
	JSON        bool
	Credentials bool
	NoRefresh   bool
	// MinExpiryMs is set only by print-bearer-token --min-expiry.
	MinExpiryMs *float64
}

// AuthCommandError is a user-facing auth command failure.
type AuthCommandError struct{ Message string }

func (e *AuthCommandError) Error() string { return e.Message }

func authCommandError(format string, args ...any) *AuthCommandError {
	return &AuthCommandError{Message: fmt.Sprintf(format, args...)}
}

var authCommandUsage = map[AuthCommandKind]string{
	AuthCommandCheck:       codingagent.AppName + " auth check --provider <provider> [--json] [--credentials] [--no-refresh]",
	AuthCommandAPIKey:      codingagent.AppName + " auth print-api-key --provider <provider> [--model <model>]",
	AuthCommandBearerToken: codingagent.AppName + " auth print-bearer-token --provider <provider> [--model <model>] [--min-expiry <duration>]",
}

// GetAuthCommandName returns the subcommand name for kind.
func GetAuthCommandName(kind AuthCommandKind) string {
	switch kind {
	case AuthCommandCheck:
		return "auth check"
	case AuthCommandAPIKey:
		return "auth print-api-key"
	default:
		return "auth print-bearer-token"
	}
}

// GetAuthCommandUsage returns the usage text for kind.
func GetAuthCommandUsage(kind AuthCommandKind) string {
	return authCommandUsage[kind]
}

// IsAuthCommandHelp reports whether args ask for auth command help.
func IsAuthCommandHelp(args []string) bool {
	if len(args) == 0 || args[0] != "auth" {
		return false
	}
	return len(args) < 2 || args[1] == "help" || slices.Contains(args, "--help") || slices.Contains(args, "-h")
}

// authCommandHelp is the `wopr auth` usage text.
const authCommandHelp = `Usage:
  wopr auth print-api-key [--provider <provider>] [--model <model>]
  wopr auth print-bearer-token [--provider <provider>] [--model <model>] [--min-expiry <duration>]
  wopr auth check [--provider <provider>] [--model <model>] [--json] [--credentials] [--no-refresh]

Auth commands require at least one of --provider or --model. Checks refresh expired OAuth credentials by default; --no-refresh prevents this. --credentials emits the credential, or includes it in JSON output.`

// ParseAuthCommand parses an auth subcommand. It returns nil for
// arguments that are not an auth command.
func ParseAuthCommand(args []string) (*AuthCommand, error) {
	if len(args) == 0 || args[0] != "auth" {
		return nil, nil
	}
	var kind AuthCommandKind
	subcommand := ""
	if len(args) > 1 {
		subcommand = args[1]
	}
	switch subcommand {
	case "check":
		kind = AuthCommandCheck
	case "print-api-key":
		kind = AuthCommandAPIKey
	case "print-bearer-token":
		kind = AuthCommandBearerToken
	default:
		return nil, authCommandError(`Unknown auth command "%s". Use "%s auth print-api-key", "%s auth print-bearer-token", or "%s auth check".`, subcommand, codingagent.AppName, codingagent.AppName, codingagent.AppName)
	}
	command := &AuthCommand{Kind: kind, Args: []string{}}
	for index := 2; index < len(args); index++ {
		arg := args[index]
		if arg == "--min-expiry" {
			if kind != AuthCommandBearerToken {
				return nil, authCommandError("--min-expiry is only supported by print-bearer-token")
			}
			index++
			value := ""
			if index < len(args) {
				value = args[index]
			}
			minExpiry, err := time.ParseDuration(value)
			if err != nil || minExpiry < 0 {
				return nil, authCommandError("--min-expiry must use a duration such as 30m or 1h")
			}
			command.MinExpiryMs = new(float64(minExpiry.Milliseconds()))
			continue
		}
		if arg == "--json" || arg == "--credentials" || arg == "--no-refresh" {
			if kind != AuthCommandCheck {
				return nil, authCommandError("%s is only supported by auth check", arg)
			}
			switch arg {
			case "--json":
				command.JSON = true
			case "--credentials":
				command.Credentials = true
			default:
				command.NoRefresh = true
			}
			continue
		}
		command.Args = append(command.Args, arg)
	}
	return command, nil
}

// AuthCommandTarget is the provider/model pair an auth command resolves.
type AuthCommandTarget struct {
	Provider string
	Model    string
}

// ValidateAuthCommandArgs validates the flags of an auth subcommand. rawArgs
// are the arguments parsed into flags, used to report the first unknown
// option in argument order.
func ValidateAuthCommandArgs(flags CLIFlags, rawArgs []string, kind AuthCommandKind) (AuthCommandTarget, error) {
	target := AuthCommandTarget{Provider: strings.TrimSpace(flags.Provider), Model: strings.TrimSpace(flags.Model)}
	if flags.UnknownOption != "" {
		return AuthCommandTarget{}, authCommandError(`Unknown option %s for "%s".`, flags.UnknownOption, GetAuthCommandName(kind))
	}
	if apiKeyFlagSet(rawArgs) || len(flags.Args) > 0 || len(flags.FileArgs) > 0 {
		return AuthCommandTarget{}, authCommandError("Auth commands only accept --provider and --model")
	}
	if target.Provider == "" && target.Model == "" {
		if kind == AuthCommandCheck {
			return AuthCommandTarget{}, authCommandError("Auth checks require --provider <provider> or --model <model>")
		}
		return AuthCommandTarget{}, authCommandError("Credential printing requires --provider <provider> or --model <model>")
	}
	return target, nil
}

// apiKeyFlagSet reports whether parseFlags consumed an --api-key value, even
// when the value is empty.
func apiKeyFlagSet(rawArgs []string) bool {
	for index, arg := range rawArgs {
		if arg == "--api-key" && index+1 < len(rawArgs) {
			return true
		}
	}
	return false
}

// authorizationBearerPattern extracts the token of an Authorization header.
var authorizationBearerPattern = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

// GetAuthCredential returns the resolved API key,
// otherwise the token of an `Authorization: Bearer` header.
func GetAuthCredential(auth *ai.AuthResult) string {
	if auth == nil {
		return ""
	}
	if auth.Auth.APIKey != "" {
		return auth.Auth.APIKey
	}
	for name, value := range auth.Auth.Headers {
		if !strings.EqualFold(name, "authorization") {
			continue
		}
		if value == nil {
			return ""
		}
		if match := authorizationBearerPattern.FindStringSubmatch(*value); match != nil {
			return match[1]
		}
		return ""
	}
	return ""
}
