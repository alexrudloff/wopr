package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alexrudloff/wopr/coding"
	codinghost "github.com/alexrudloff/wopr/coding/host"
	"github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/serve"
)

// serveUsage is the flag list every help and error line prints.
const serveUsage = "wopr serve [--port N] [--hostname H] [--dir PATH] [--learn-max-models N] [--learn-ttl DAYS] [--max-history-tokens N]"

func runServeCommand(args []string) int {
	if len(args) == 0 || args[0] != "serve" {
		return -1
	}
	args = args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help":
			fmt.Println("Usage:\n  " + serveUsage + "\n\nStart the wopr serve daemon.")
			return 0
		}
	}

	flagPort := 0
	flagHost := ""

	var portVal, hostVal, dirVal, learnMaxVal, learnTTLVal, maxHistoryVal string
	stringFlags := map[string]*string{
		"--port":               &portVal,
		"--hostname":           &hostVal,
		"--dir":                &dirVal,
		"--learn-max-models":   &learnMaxVal,
		"--learn-ttl":          &learnTTLVal,
		"--max-history-tokens": &maxHistoryVal,
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if p, ok := stringFlags[arg]; ok {
			if i+1 >= len(args) || args[i+1] == "" {
				fmt.Fprintf(os.Stderr, "wopr serve: %s requires a non-empty value\n", arg)
				fmt.Fprintln(os.Stderr, "Usage: "+serveUsage)
				return 2
			}
			i++
			*p = args[i]
			continue
		}
		switch {
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "wopr serve: unknown option: %s\n", arg)
			fmt.Fprintln(os.Stderr, "Usage: "+serveUsage)
			return 2
		default:
			fmt.Fprintf(os.Stderr, "wopr serve: unexpected argument: %s\n", arg)
			fmt.Fprintln(os.Stderr, "Usage: "+serveUsage)
			return 2
		}
	}

	if portVal != "" {
		p, err := strconv.Atoi(portVal)
		if err != nil || p <= 0 {
			fmt.Fprintf(os.Stderr, "wopr serve: --port requires a positive integer\n")
			fmt.Fprintln(os.Stderr, "Usage: "+serveUsage)
			return 2
		}
		flagPort = p
	}
	flagHost = hostVal

	maxModels, ttlDays := 0, 0
	if learnMaxVal != "" {
		n, err := strconv.Atoi(learnMaxVal)
		if err != nil || n <= 0 {
			fmt.Fprintf(os.Stderr, "wopr serve: --learn-max-models requires a positive integer\n")
			fmt.Fprintln(os.Stderr, "Usage: "+serveUsage)
			return 2
		}
		maxModels = n
	}
	if learnTTLVal != "" {
		n, err := strconv.Atoi(learnTTLVal)
		if err != nil || n <= 0 {
			fmt.Fprintf(os.Stderr, "wopr serve: --learn-ttl requires a positive integer (days)\n")
			fmt.Fprintln(os.Stderr, "Usage: "+serveUsage)
			return 2
		}
		ttlDays = n
	}

	var maxHistoryTokens *int
	if maxHistoryVal != "" {
		n, err := strconv.Atoi(maxHistoryVal)
		if err != nil || n < 0 {
			fmt.Fprintf(os.Stderr, "wopr serve: --max-history-tokens requires a non-negative integer (0 disables)\n")
			fmt.Fprintln(os.Stderr, "Usage: "+serveUsage)
			return 2
		}
		v := n
		maxHistoryTokens = &v
	}

	agentDir := codingagent.AgentDir()
	// The allowlist is a global-layer guardrail: a served project must not be
	// able to widen it. The manager's cwd is irrelevant to global settings.
	globalSettings := codingagent.NewSettingsManagerWithProjectTrust(".", agentDir, false)
	for _, key := range globalSettings.GlobalUnknownServerKeys() {
		fmt.Fprintf(os.Stderr, "wopr serve: warning: unrecognized server setting %q ignored\n", key)
	}
	allowedRoots, err := globalServeAllowedRoots(globalSettings)
	if err != nil {
		return failf("wopr serve: %v", err)
	}
	cwd, err := resolveServeRoot(dirVal, allowedRoots)
	if err != nil {
		return failf("wopr serve: %v", err)
	}
	projectTrusted, err := resolveProjectTrusted(context.Background(), projectTrustResolutionOptions{
		CWD: cwd, Store: codingagent.NewProjectTrustStore(agentDir),
		Default: globalSettings.GetDefaultProjectTrust(),
	})
	if err != nil {
		return failf("wopr serve: %v", err)
	}
	settings := codingagent.NewSettingsManagerWithProjectTrust(cwd, agentDir, projectTrusted)
	reportSettingsErrors(settings, "serve")

	port := settings.Get().GetServerPort()
	if flagPort != 0 {
		port = flagPort
	}
	host := settings.Get().GetServerHostname()
	if flagHost != "" {
		host = flagHost
	}

	if nonLoopbackBind(host) {
		fmt.Fprintf(os.Stderr, "wopr serve: no authentication. Anyone who can reach %s can run tools and read files as this user. Binding to a LAN is not a security control.\n", net.JoinHostPort(host, strconv.Itoa(port)))
	}

	// Build Services.
	services, err := coding.NewServices(coding.ServicesOptions{
		CWD:              cwd,
		AgentDir:         agentDir,
		ProjectTrusted:   func() *bool { b := true; return &b }(),
		LearnMaxModels:   maxModels,
		LearnTTLDays:     ttlDays,
		MaxHistoryTokens: maxHistoryTokens,
	})
	if err != nil {
		return failf("wopr serve: %v", err)
	}

	// Build Host.
	hostObj := codinghost.New(services, nil)

	// Resolve session directory once.
	sessionDir, err := resolveSessionDir("", settings)
	if err != nil {
		return failf("wopr serve: %v", err)
	}
	if sessionDir == "" {
		sessionDir = codingagent.NewSessionManager(cwd).SessionDir()
	}

	// Resolve model spec. When no provider is authenticated and no default
	// resolves, selectStartupModel returns a nil model with a nil error. Leave
	// modelSpec empty: GET /config reports no model and POST /session answers 409
	// (codinghost.ErrNoModel), instead of naming a provider this server cannot
	// reach.
	startup, err := selectStartupModel(startupModelOptions{}, settings.Get(), services)
	if err != nil {
		return failf("wopr serve: %v", err)
	}
	var modelSpec string
	if startup.Model != nil {
		modelSpec = codinghost.ModelProviderID(startup.Model) + "/" + startup.Model.ID
	}

	srv, ln, err := serve.New(net.JoinHostPort(host, strconv.Itoa(port)), hostObj, sessionDir, modelSpec)
	if err != nil {
		return failf("wopr serve: %v", err)
	}

	fmt.Fprintf(os.Stderr, "wopr serve listening on http://%s\n", ln.Addr().String())
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return failf("wopr serve: %v", err)
	}
	return 0
}

// globalServeAllowedRoots returns the global-layer serve allowlist. A global
// settings file that is present but cannot be parsed is an error, so wopr
// serve fails closed rather than serving unrestricted. An absent or
// successfully parsed layer with an empty server.allowedRoots stays
// unrestricted (nil, nil).
func globalServeAllowedRoots(sm *codingagent.SettingsManager) ([]string, error) {
	if err := sm.GlobalLoadError(); err != nil {
		return nil, fmt.Errorf("global settings: %w", err)
	}
	return sm.GetGlobalSettings().GetServerAllowedRoots()
}

// resolveServeRoot returns the directory wopr serve will serve: --dir when
// given, else the invocation cwd. When allowedRoots is non-empty the resolved
// root must be contained in one entry; the entries arrive already expanded
// (~ / file://) from GetServerAllowedRoots. Containment is per path component,
// never a raw string prefix. An unresolvable entry fails closed.
func resolveServeRoot(dirFlag string, allowedRoots []string) (string, error) {
	root, err := resolveWorkingDir(dirFlag)
	if err != nil {
		return "", err
	}
	if len(allowedRoots) == 0 {
		return root, nil
	}
	// The default root is os.Getwd(), which can be a symlinked path. Resolve it
	// before comparing, or a cwd reached through a symlink escapes containment.
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("--dir %q: %w", root, err)
	}
	for _, entry := range allowedRoots {
		entryAbs, err := filepath.Abs(entry)
		if err != nil {
			return "", fmt.Errorf("server.allowedRoots: %q: %w", entry, err)
		}
		entryResolved, err := filepath.EvalSymlinks(entryAbs)
		if err != nil {
			return "", fmt.Errorf("server.allowedRoots: %q is not a valid directory: %w", entry, err)
		}
		info, err := os.Stat(entryResolved)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("server.allowedRoots: %q is not a directory", entry)
		}
		rel, err := filepath.Rel(entryResolved, root)
		if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))) {
			return root, nil
		}
	}
	return "", fmt.Errorf("--dir %q is outside every server.allowedRoots entry", root)
}

func nonLoopbackBind(host string) bool {
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}
