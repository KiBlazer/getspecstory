package utils

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var (
	forkVersionURL         = "https://raw.githubusercontent.com/KiBlazer/getspecstory/release/specstory-cli/VERSION"
	versionCheckHTTPClient = &http.Client{Timeout: 2500 * time.Millisecond}
)

func latestForkVersion() (string, error) {
	resp, err := versionCheckHTTPClient.Get(forkVersionURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(body))
	if version == "" {
		return "", fmt.Errorf("empty version file")
	}
	return version, nil
}

// CheckForUpdates checks for newer versions of the CLI and displays a notification if available
func CheckForUpdates(currentVersion string, noVersionCheck bool, silent bool) {
	if noVersionCheck || currentVersion == "dev" {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			// Silently recover from any panic during version check
			slog.Error("Version check panicked", "error", r)
		}
	}()

	latestVersion, err := latestForkVersion()
	if err != nil {
		slog.Error("Version check failed", "error", err)
		return
	}

	// Simple version comparison - if versions are different, assume newer
	// This is a simple check; for more complex versioning, we'd need semantic version parsing
	if latestVersion != currentVersion {
		if !silent {
			fmt.Println()
			fmt.Println("╭─────────────────────────────────────────────────────────────╮")
			// Check if current version contains "beta"
			if regexp.MustCompile(`(?i)beta`).MatchString(currentVersion) {
				fmt.Println("│                  Beta Version in use! 🚀                    │")
			} else {
				fmt.Println("│                   Update Available! 🚀                      │")
			}
			fmt.Println("├─────────────────────────────────────────────────────────────┤")
			fmt.Printf("│ Current version: %-42s │\n", currentVersion)
			fmt.Printf("│ Latest version:  %-42s │\n", latestVersion)
			fmt.Println("├─────────────────────────────────────────────────────────────┤")
			fmt.Println("│ Run `specstory update` to install the new version.          │")
			fmt.Println("╰─────────────────────────────────────────────────────────────╯")
			fmt.Println()
		}
	}
}
