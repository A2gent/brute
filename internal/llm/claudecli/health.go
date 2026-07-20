package claudecli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	defaultHealthTimeout = 5 * time.Second
	maxHealthOutputBytes = 256 * 1024
)

var emailRedactRe = regexp.MustCompile(`^([^@]{1,3})[^@]*(@.+)$`)

// HealthStatus describes Claude CLI availability without running inference.
type HealthStatus string

const (
	HealthHealthy     HealthStatus = "healthy"
	HealthDegraded    HealthStatus = "degraded"
	HealthUnavailable HealthStatus = "unavailable"
)

// HealthReport is the safe health probe result.
type HealthReport struct {
	Status       HealthStatus `json:"status"`
	Version      string       `json:"version,omitempty"`
	Account      string       `json:"account,omitempty"`
	Subscription string       `json:"subscription,omitempty"`
	TokenSource  string       `json:"token_source,omitempty"`
	APIProvider  string       `json:"api_provider,omitempty"`
	Models       []string     `json:"models,omitempty"`
	Commands     []string     `json:"commands,omitempty"`
	CheckedAt    time.Time    `json:"checked_at"`
	ExpiresAt    time.Time    `json:"expires_at,omitempty"`
	Cached       bool         `json:"cached"`
	Error        string       `json:"error,omitempty"`
}

type authStatusJSON struct {
	LoggedIn         bool   `json:"loggedIn"`
	Email            string `json:"email"`
	Subscription     string `json:"subscription"`
	SubscriptionType string `json:"subscriptionType"`
	TokenSource      string `json:"tokenSource"`
	AuthMethod       string `json:"authMethod"`
	APIProvider      string `json:"apiProvider"`
}

// CommandRunner executes external commands for health probes and tests.
type CommandRunner interface {
	Run(ctx context.Context, binary string, args []string, env []string) (stdout string, stderr string, err error)
}

type cappedStringWriter struct {
	builder strings.Builder
	remain  int
}

func (w *cappedStringWriter) Write(p []byte) (int, error) {
	originalLen := len(p)
	if w.remain > 0 {
		if len(p) > w.remain {
			p = p[:w.remain]
		}
		_, _ = w.builder.Write(p)
		w.remain -= len(p)
	}
	return originalLen, nil
}

func (w *cappedStringWriter) String() string { return w.builder.String() }

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, binary string, args []string, env []string) (string, string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	if len(env) > 0 {
		cmd.Env = env
	}
	stdout := cappedStringWriter{remain: maxHealthOutputBytes}
	stderr := cappedStringWriter{remain: maxHealthOutputBytes}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// ProbeHealth runs safe Claude CLI health checks without inference.
func ProbeHealth(ctx context.Context, options Options, runner CommandRunner) HealthReport {
	report := HealthReport{CheckedAt: time.Now().UTC(), Status: HealthUnavailable}
	if runner == nil {
		runner = execCommandRunner{}
	}

	binary, err := ResolveExecutable(options.Executable)
	if err != nil {
		report.Error = "Claude CLI executable was not found"
		return report
	}
	env := options.Environment
	if env == nil {
		env = os.Environ()
	}

	versionCtx, cancelVersion := context.WithTimeout(ctx, defaultHealthTimeout)
	versionOut, _, versionErr := runner.Run(versionCtx, binary, []string{"--version"}, env)
	cancelVersion()
	version := firstOutputLine(versionOut)
	if versionErr != nil || version == "" {
		report.Error = "Claude CLI version check failed"
		return report
	}
	report.Version = version

	authCtx, cancelAuth := context.WithTimeout(ctx, defaultHealthTimeout)
	authOut, _, authErr := runner.Run(authCtx, binary, []string{"auth", "status", "--json"}, env)
	cancelAuth()

	loggedIn := false
	if authErr == nil {
		var auth authStatusJSON
		if json.Unmarshal([]byte(strings.TrimSpace(authOut)), &auth) == nil {
			loggedIn = auth.LoggedIn
			report.Account = redactEmail(auth.Email)
			report.Subscription = firstNonEmpty(strings.TrimSpace(auth.Subscription), strings.TrimSpace(auth.SubscriptionType))
			report.TokenSource = firstNonEmpty(strings.TrimSpace(auth.TokenSource), strings.TrimSpace(auth.AuthMethod))
			report.APIProvider = strings.TrimSpace(auth.APIProvider)
		}
	}

	helpCtx, cancelHelp := context.WithTimeout(ctx, defaultHealthTimeout)
	helpOut, _, _ := runner.Run(helpCtx, binary, []string{"--help"}, env)
	cancelHelp()
	report.Models, report.Commands = parseHelpOutput(helpOut)

	if loggedIn {
		report.Status = HealthHealthy
		return report
	}
	report.Status = HealthDegraded
	if authErr != nil {
		report.Error = "Claude CLI auth status is unavailable"
	}
	return report
}

func redactEmail(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	if parts := emailRedactRe.FindStringSubmatch(email); len(parts) == 3 {
		return parts[1] + "***" + parts[2]
	}
	return "***"
}

func firstOutputLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if len(line) > 256 {
				return line[:256]
			}
			return line
		}
	}
	return ""
}

// ContinuationIdentity returns a deterministic identity for Claude session continuation.
func ContinuationIdentity(configDir string) string {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		return ""
	}
	sum := sha256Hex(configDir)
	return fmt.Sprintf("claude:%s", sum[:16])
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// ResolveExecutable picks provider binary before global fallback env.
func ResolveExecutable(providerBinary string) (string, error) {
	providerBinary = strings.TrimSpace(providerBinary)
	if providerBinary != "" {
		return findExecutable(providerBinary)
	}
	return findExecutable("")
}
