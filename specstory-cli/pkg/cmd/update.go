// Package cmd contains CLI command implementations for the SpecStory CLI.
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/specstoryai/getspecstory/specstory-cli/pkg/analytics"
)

const (
	// DefaultRepoURL is the default repository to pull updates from
	DefaultRepoURL = "https://github.com/KiBlazer/getspecstory.git"
	// DefaultBranch is the default branch to pull from
	DefaultBranch = "dev"
)

// CreateUpdateCommand creates the update command.
// The update command pulls the latest code from the specified repository,
// builds the binary, and installs it to the appropriate location.
func CreateUpdateCommand(version string) *cobra.Command {
	var repoURL string
	var branch string
	var installDir string

	updateCmd := &cobra.Command{
		Use:   "update",
		Short: "Update SpecStory CLI to the latest version",
		Long: `Pull the latest code from the repository, build the binary, and install it.

By default, pulls from ` + DefaultRepoURL + ` (` + DefaultBranch + ` branch)
and installs to ~/bin/specstory.`,
		Example: `# Update from default repository
specstory update

# Update from a specific branch
specstory update --branch main

# Update from a custom repository
specstory update --repo https://github.com/user/getspecstory.git

# Install to a custom directory
specstory update --install-dir /usr/local/bin`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Track update command usage
			analytics.TrackEvent(analytics.EventUpdateCommand, analytics.Properties{
				"version": version,
			})

			// Resolve install directory
			if installDir == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("failed to get home directory: %w", err)
				}
				installDir = filepath.Join(home, "bin")
			}

			// Check if go is installed
			if _, err := exec.LookPath("go"); err != nil {
				return fmt.Errorf("go is not installed or not in PATH: %w", err)
			}

			// Check if git is installed
			if _, err := exec.LookPath("git"); err != nil {
				return fmt.Errorf("git is not installed or not in PATH: %w", err)
			}

			fmt.Printf("Updating SpecStory CLI from %s (%s branch)...\n", repoURL, branch)

			// Create temporary directory for cloning
			tmpDir, err := os.MkdirTemp("", "specstory-update-*")
			if err != nil {
				return fmt.Errorf("failed to create temp directory: %w", err)
			}
			defer os.RemoveAll(tmpDir)

			// Clone the repository
			fmt.Printf("Cloning repository...\n")
			cloneCmd := exec.Command("git", "clone", "--depth", "1", "--branch", branch, repoURL, tmpDir)
			cloneCmd.Stdout = os.Stdout
			cloneCmd.Stderr = os.Stderr
			if err := cloneCmd.Run(); err != nil {
				return fmt.Errorf("failed to clone repository: %w", err)
			}

			// Build the binary
			fmt.Println("Building SpecStory CLI...")
			buildDir := filepath.Join(tmpDir, "specstory-cli")
			binaryName := "specstory"
			if runtime.GOOS == "windows" {
				binaryName = "specstory.exe"
			}

			// Do not inject a Git SHA as the version. The cloned source declares
			// the fork's semantic version in main.version, and release builds can
			// still override it with their own ldflags.
			buildCmd := exec.Command("go", "build", "-ldflags", "-s -w", "-o", binaryName)
			buildCmd.Dir = buildDir
			buildCmd.Stdout = os.Stdout
			buildCmd.Stderr = os.Stderr
			if err := buildCmd.Run(); err != nil {
				return fmt.Errorf("failed to build binary: %w", err)
			}

			// Ensure install directory exists
			if err := os.MkdirAll(installDir, 0755); err != nil {
				return fmt.Errorf("failed to create install directory: %w", err)
			}

			// Try to install the binary directly
			srcPath := filepath.Join(buildDir, binaryName)
			dstPath := filepath.Join(installDir, binaryName)

			// Read source binary
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return fmt.Errorf("failed to read built binary: %w", err)
			}

			// Try to write to destination
			err = os.WriteFile(dstPath, data, 0755)
			if err != nil {
				// If the binary is busy (running), save to a temporary location
				// and provide instructions
				tmpDst := dstPath + ".new"
				if err := os.WriteFile(tmpDst, data, 0755); err != nil {
					return fmt.Errorf("failed to save new binary: %w", err)
				}

				fmt.Printf("\n⚠️  Could not replace running binary at %s\n", dstPath)
				fmt.Printf("   New binary saved to: %s\n", tmpDst)
				fmt.Printf("\n   To complete the update, run:\n")
				fmt.Printf("   mv %s %s\n", tmpDst, dstPath)
				return nil
			}

			fmt.Printf("\n✅ SpecStory CLI updated successfully!\n")
			fmt.Printf("   Installed to: %s\n", dstPath)

			// Get new version
			newVersionCmd := exec.Command(dstPath, "version")
			output, err := newVersionCmd.Output()
			if err == nil {
				fmt.Printf("   Version: %s", string(output))
			}

			return nil
		},
	}

	// Define flags
	updateCmd.Flags().StringVar(&repoURL, "repo", DefaultRepoURL, "repository URL to pull from")
	updateCmd.Flags().StringVar(&branch, "branch", DefaultBranch, "branch to pull from")
	updateCmd.Flags().StringVar(&installDir, "install-dir", "", "directory to install binary (default: ~/bin)")

	return updateCmd
}
