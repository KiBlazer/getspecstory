package opencode

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"log/slog"

	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi"
)

// Package-level variables for mocking in tests
var (
	execLookPath = exec.LookPath
)

var errNoVersionOutput = errors.New("opencode version command produced no output")

// parseOpenCodeCommand splits a custom command string into the binary path and its arguments.
// An empty custom command falls back to the detected default binary.
func parseOpenCodeCommand(customCommand string) (string, []string) {
	if customCommand != "" {
		parts := spi.SplitCommandLine(customCommand)
		if len(parts) > 0 {
			return expandTilde(parts[0]), parts[1:]
		}
	}

	return getDefaultOpenCodeCommand(), nil
}

// getDefaultOpenCodeCommand looks for the opencode binary in common installation locations.
func getDefaultOpenCodeCommand() string {
	if path, ok := findNpmOpenCode(); ok {
		return path
	}

	return "opencode"
}

// findNpmOpenCode returns the opencode binary from global npm locations if present.
func findNpmOpenCode() (string, bool) {
	// Prefer explicit NVM_BIN if it points to the opencode executable.
	if nvmBin := strings.TrimSpace(os.Getenv("NVM_BIN")); nvmBin != "" {
		candidate := filepath.Join(nvmBin, "opencode")
		if isExecutable(candidate) {
			slog.Debug("OpenCode: Found npm binary via NVM_BIN", "path", candidate)
			return candidate, true
		}
	}

	npmPath, err := execLookPath("npm")
	if err == nil {
		var out bytes.Buffer
		cmd := exec.Command(npmPath, "bin", "-g")
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err == nil {
			binDir := strings.TrimSpace(out.String())
			if binDir != "" {
				candidate := filepath.Join(binDir, "opencode")
				if isExecutable(candidate) {
					slog.Debug("OpenCode: Found npm binary via npm bin -g", "path", candidate)
					return candidate, true
				}
			}
		}
	}

	// Fallback to common NVM layout if we know NVM_DIR.
	if nvmDir := strings.TrimSpace(os.Getenv("NVM_DIR")); nvmDir != "" {
		candidate := filepath.Join(nvmDir, "versions", "node", "current", "bin", "opencode")
		if isExecutable(candidate) {
			slog.Debug("OpenCode: Found npm binary via NVM_DIR fallback", "path", candidate)
			return candidate, true
		}
	}

	return "", false
}

// expandTilde expands a leading tilde in a path to the user's home directory.
func expandTilde(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}

	home, err := osUserHomeDir()
	if err != nil {
		return path
	}

	if path == "~" {
		return home
	}

	if len(path) >= 2 && (path[1] == '/' || path[1] == '\\') {
		return filepath.Join(home, path[2:])
	}

	return path
}

// isExecutable returns true if the file exists and has execute permissions.
func isExecutable(path string) bool {
	info, err := osStat(path)
	if err != nil {
		return false
	}

	return !info.IsDir() && info.Mode()&0o111 != 0
}

// runOpenCodeVersionCommand tries common version flags and returns the first successful output.
func runOpenCodeVersionCommand(command string) (string, string, string, error) {
	flags := []string{"--version", "-V"}
	var lastErr error
	var lastStderr string

	for idx, flag := range flags {
		var stdout bytes.Buffer
		var stderr bytes.Buffer

		cmd := exec.Command(command, flag)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		stderrStr := strings.TrimSpace(stderr.String())
		if err != nil {
			lastErr = err
			lastStderr = stderrStr

			// For fatal errors (binary missing/permission issues) or last attempt, stop immediately.
			if classifyCheckError(err) != "unknown" || idx == len(flags)-1 {
				return "", flag, lastStderr, err
			}
			continue
		}

		output := strings.TrimSpace(stdout.String())
		if output == "" {
			return "", flag, stderrStr, errNoVersionOutput
		}

		return output, flag, stderrStr, nil
	}

	if lastErr != nil {
		return "", flags[len(flags)-1], lastStderr, lastErr
	}

	return "", "", lastStderr, errors.New("failed to execute opencode version command")
}

// classifyCheckError buckets common error categories for user guidance and analytics.
func classifyCheckError(err error) string {
	if err == nil {
		return ""
	}

	var execErr *exec.Error
	if errors.As(err, &execErr) && execErr.Err == exec.ErrNotFound {
		return "not_found"
	}

	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		if errors.Is(pathErr.Err, os.ErrNotExist) {
			return "not_found"
		}
		if errors.Is(pathErr.Err, os.ErrPermission) {
			return "permission_denied"
		}
	}

	if errors.Is(err, os.ErrPermission) {
		return "permission_denied"
	}

	if errors.Is(err, errNoVersionOutput) {
		return "no_output"
	}

	return "unknown"
}

// ExecuteOpenCode executes OpenCode in interactive mode and blocks until it exits.
// The customCommand parameter specifies the opencode binary and optional arguments to use.
// If customCommand is empty, falls back to the detected default opencode binary.
// If resumeSessionID is provided, runs "opencode --resume <sessionId>" to continue that session.
// Otherwise, starts a new opencode session.
func ExecuteOpenCode(customCommand string, resumeSessionID string) error {
	// Parse the command and get binary + args
	opencodeCmd, args := parseOpenCodeCommand(customCommand)

	// Append resume args if resuming a session
	if resumeSessionID != "" {
		args = append(args, "--resume", resumeSessionID)
		slog.Info("ExecuteOpenCode: Resuming OpenCode session",
			"command", opencodeCmd,
			"sessionID", resumeSessionID,
			"customArgs", args)
	} else {
		slog.Info("ExecuteOpenCode: Starting new OpenCode session",
			"command", opencodeCmd,
			"args", args)
	}

	cmd := exec.Command(opencodeCmd, args...)

	// Configure interactive mode - connect to user's terminal
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Run the command and wait for it to complete
	slog.Info("ExecuteOpenCode: Executing OpenCode (blocking until exit)")
	if err := cmd.Run(); err != nil {
		slog.Error("ExecuteOpenCode: OpenCode execution failed", "error", err)
		return fmt.Errorf("opencode execution failed: %w", err)
	}

	slog.Info("ExecuteOpenCode: OpenCode exited successfully")
	return nil
}
