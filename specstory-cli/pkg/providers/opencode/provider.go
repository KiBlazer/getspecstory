package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/specstoryai/getspecstory/specstory-cli/pkg/analytics"
	"github.com/specstoryai/getspecstory/specstory-cli/pkg/log"
	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi"
)

// Provider implements the SPI Provider interface for OpenCode.
type Provider struct{}

// NewProvider creates a new OpenCode provider instance.
func NewProvider() *Provider {
	return &Provider{}
}

// Name returns the human-readable name of this provider.
func (p *Provider) Name() string {
	return "OpenCode"
}

// buildCheckErrorMessage creates a user-facing error message tailored to the failure type.
func buildCheckErrorMessage(errorType string, opencodeCmd string, isCustom bool, stderrOutput string) string {
	var errorMsg strings.Builder

	switch errorType {
	case "not_found":
		fmt.Fprintf(&errorMsg, "  Could not find OpenCode at: %s\n\n", opencodeCmd)
		errorMsg.WriteString("  Here's how to fix this:\n\n")
		if isCustom {
			errorMsg.WriteString("     - Double-check the custom command/path you provided\n")
			errorMsg.WriteString("     - Ensure the file exists and is executable\n")
			errorMsg.WriteString("     - If the binary lives elsewhere, point to it with an absolute path\n")
		} else {
			errorMsg.WriteString("     1. Check common installation locations:\n")
			errorMsg.WriteString("        - npm: $(npm bin -g)/opencode (installed via `npm install -g opencode`)\n")
			errorMsg.WriteString("\n")
			errorMsg.WriteString("     2. If it's already installed, try:\n")
			errorMsg.WriteString("        - Check if 'opencode' is in your PATH\n")
			errorMsg.WriteString("        - Use -c flag to specify the full path\n")
			errorMsg.WriteString("        - Example: specstory check opencode -c \"/usr/local/bin/opencode\"")
		}
	case "permission_denied":
		fmt.Fprintf(&errorMsg, "  Permission denied when trying to run: %s\n\n", opencodeCmd)
		errorMsg.WriteString("  Try the following:\n")
		fmt.Fprintf(&errorMsg, "     - Ensure the binary is executable: chmod +x %s\n", opencodeCmd)
		errorMsg.WriteString("     - Run the command manually to confirm it works outside SpecStory\n")
	case "no_output":
		errorMsg.WriteString("  No version information from opencode\n\n")
		errorMsg.WriteString("  The command ran but produced no output\n")
		errorMsg.WriteString("  Expected: Version information from opencode\n\n")
		errorMsg.WriteString("  Please verify you're pointing at the OpenCode binary:\n")
		errorMsg.WriteString("     - Try running '" + opencodeCmd + " --version' directly\n")
		errorMsg.WriteString("     - If you're using a wrapper script, pass the real opencode binary with -c\n")
	default:
		fmt.Fprintf(&errorMsg, "  Error running '%s --version'\n", opencodeCmd)
		if stderrOutput != "" {
			fmt.Fprintf(&errorMsg, "  Error details: %s\n", stderrOutput)
		}
		errorMsg.WriteString("\n")
		errorMsg.WriteString("  Troubleshooting tips:\n")
		errorMsg.WriteString("     - Make sure OpenCode is correctly installed\n")
		errorMsg.WriteString("     - Try running 'opencode --version' directly in your terminal\n")
	}

	return errorMsg.String()
}

// Check verifies OpenCode installation and returns version info.
func (p *Provider) Check(customCommand string) spi.CheckResult {
	opencodeCmd, _ := parseOpenCodeCommand(customCommand)
	isCustomCommand := customCommand != ""

	resolvedPath := opencodeCmd
	if !filepath.IsAbs(opencodeCmd) {
		if path, err := execLookPath(opencodeCmd); err == nil {
			resolvedPath = path
		}
	}

	versionOutput, versionFlag, stderrOutput, err := runOpenCodeVersionCommand(opencodeCmd)
	if err != nil {
		errorType := classifyCheckError(err)
		analytics.TrackEvent(analytics.EventCheckInstallFailed, analytics.Properties{
			"provider":       "opencode",
			"custom_command": isCustomCommand,
			"command_path":   opencodeCmd,
			"resolved_path":  resolvedPath,
			"error_type":     errorType,
			"version_flag":   versionFlag,
			"stderr":         stderrOutput,
			"error_message":  err.Error(),
		})

		errorMessage := buildCheckErrorMessage(errorType, opencodeCmd, isCustomCommand, stderrOutput)

		return spi.CheckResult{
			Success:      false,
			Version:      "",
			Location:     resolvedPath,
			ErrorMessage: errorMessage,
		}
	}

	if versionOutput == "" {
		errorType := "no_output"
		analytics.TrackEvent(analytics.EventCheckInstallFailed, analytics.Properties{
			"provider":       "opencode",
			"custom_command": isCustomCommand,
			"command_path":   opencodeCmd,
			"resolved_path":  resolvedPath,
			"error_type":     errorType,
			"version_flag":   versionFlag,
			"stderr":         stderrOutput,
		})

		errorMessage := buildCheckErrorMessage(errorType, opencodeCmd, isCustomCommand, stderrOutput)

		return spi.CheckResult{
			Success:      false,
			Version:      "",
			Location:     resolvedPath,
			ErrorMessage: errorMessage,
		}
	}

	analytics.TrackEvent(analytics.EventCheckInstallSuccess, analytics.Properties{
		"provider":       "opencode",
		"custom_command": isCustomCommand,
		"command_path":   resolvedPath,
		"version":        versionOutput,
		"version_flag":   versionFlag,
	})

	slog.Debug("OpenCode check successful", "version", versionOutput, "location", resolvedPath, "flag", versionFlag)

	return spi.CheckResult{
		Success:      true,
		Version:      versionOutput,
		Location:     resolvedPath,
		ErrorMessage: "",
	}
}

// DetectAgent checks if OpenCode has been used in the given project path.
func (p *Provider) DetectAgent(projectPath string, helpOutput bool) bool {
	// Try to find sessions using the shared helper (stop on first match)
	sessions, err := findOpenCodeSessions(projectPath, "", true)

	// Handle different error cases with helpful output
	if err != nil {
		// Check if it's a database not found error
		if strings.Contains(err.Error(), "database not found") {
			slog.Debug("DetectAgent: OpenCode database not found", "error", err)
			if helpOutput {
				fmt.Println()
				log.UserWarn("No OpenCode sessions were found for this directory.\n")
				log.UserMessage("OpenCode stores sessions in a SQLite database at ~/.local/share/opencode/opencode.db.\n")
				log.UserMessage("We couldn't find that database file.\n\n")
				log.UserMessage("To fix this:\n")
				log.UserMessage("  1. Run `specstory run opencode` to launch OpenCode in this project\n")
				log.UserMessage("  2. Or start OpenCode manually, then run `specstory sync opencode` afterward\n\n")
				fmt.Println()
			}
			return false
		}

		// Unknown error
		slog.Debug("DetectAgent: Error finding sessions", "error", err)
		return false
	}

	// Check if we found any sessions
	if len(sessions) > 0 {
		session := sessions[0]
		slog.Debug("DetectAgent: OpenCode activity detected",
			"sessionID", session.SessionID,
			"directory", session.Directory)
		return true
	}

	// No sessions found - provide helpful output
	if helpOutput {
		fmt.Println()
		log.UserWarn("No OpenCode sessions were found for this directory.\n")
		log.UserMessage("OpenCode hasn't saved a session with this working directory yet.\n")
		log.UserMessage("OpenCode stores sessions in a SQLite database at ~/.local/share/opencode/opencode.db.\n\n")
		log.UserMessage("To fix this:\n")
		log.UserMessage("  1. Run `specstory run opencode` to launch OpenCode here\n")
		log.UserMessage("  2. Or open OpenCode manually in this project, then try syncing again\n\n")
		fmt.Println()
	}

	return false
}

// GetAgentChatSessions retrieves chat sessions for the given project path.
func (p *Provider) GetAgentChatSessions(projectPath string, debugRaw bool, progress spi.ProgressCallback) ([]spi.AgentChatSession, error) {
	// Find all sessions for this project (don't stop on first)
	sessions, err := findOpenCodeSessions(projectPath, "", false)
	if err != nil {
		// If database doesn't exist, return empty list (not an error)
		if strings.Contains(err.Error(), "database not found") {
			return []spi.AgentChatSession{}, nil
		}
		return nil, fmt.Errorf("failed to find opencode sessions: %w", err)
	}

	totalSessions := len(sessions)

	// Open database for reading session data
	dbPath, err := resolveDBPath()
	if err != nil {
		return nil, err
	}
	db, err := openDBReadOnly(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Convert to AgentChatSession structs with progress reporting
	var result []spi.AgentChatSession
	for i, sessionInfo := range sessions {
		session, err := processSessionToAgentChat(db, &sessionInfo, projectPath, debugRaw)
		if err != nil {
			slog.Debug("GetAgentChatSessions: Failed to process session",
				"sessionID", sessionInfo.SessionID,
				"error", err)
			// Report progress even for failed sessions
			if progress != nil {
				progress(i+1, totalSessions)
			}
			continue // Skip sessions we can't process
		}

		// Skip empty sessions
		if session == nil {
			slog.Debug("GetAgentChatSessions: Skipping empty session",
				"sessionID", sessionInfo.SessionID)
			// Report progress even for empty sessions
			if progress != nil {
				progress(i+1, totalSessions)
			}
			continue
		}

		result = append(result, *session)

		// Report progress after each session
		if progress != nil {
			progress(i+1, totalSessions)
		}
	}

	return result, nil
}

// GetAgentChatSession retrieves a chat session by ID.
func (p *Provider) GetAgentChatSession(projectPath string, sessionID string, debugRaw bool) (*spi.AgentChatSession, error) {
	// Find the specific session
	sessions, err := findOpenCodeSessions(projectPath, sessionID, false)
	if err != nil {
		// If database doesn't exist, return nil (not an error)
		if strings.Contains(err.Error(), "database not found") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find opencode sessions: %w", err)
	}

	// Session not found (empty result)
	if len(sessions) == 0 {
		return nil, nil
	}

	// Open database for reading session data
	dbPath, err := resolveDBPath()
	if err != nil {
		return nil, err
	}
	db, err := openDBReadOnly(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Process the first (and only) session
	return processSessionToAgentChat(db, &sessions[0], projectPath, debugRaw)
}

// ExecAgentAndWatch executes OpenCode in interactive mode and monitors for session updates.
func (p *Provider) ExecAgentAndWatch(projectPath string, customCommand string, resumeSessionID string, debugRaw bool, sessionCallback func(*spi.AgentChatSession)) error {
	slog.Info("ExecAgentAndWatch: Starting OpenCode execution and monitoring",
		"projectPath", projectPath,
		"customCommand", customCommand,
		"resumeSessionID", resumeSessionID,
		"debugRaw", debugRaw)

	// Log if resuming a specific session
	if resumeSessionID != "" {
		slog.Info("ExecAgentAndWatch: Will resume specific OpenCode session", "sessionID", resumeSessionID)
	}

	// Set up the callback for real-time session updates
	SetWatcherCallback(sessionCallback)
	defer ClearWatcherCallback()

	// Set debug raw mode for the watcher
	SetWatcherDebugRaw(debugRaw)

	// Start watching for OpenCode sessions in the background
	slog.Info("Initializing OpenCode session monitoring...")

	if err := WatchForOpenCodeSessions(projectPath, resumeSessionID); err != nil {
		// Log the error but don't fail - watcher might work later
		slog.Error("Failed to start OpenCode session watcher", "error", err)
	}

	// Execute OpenCode - this blocks until OpenCode exits
	slog.Info("Executing OpenCode", "command", customCommand, "resumeSessionID", resumeSessionID)
	err := ExecuteOpenCode(customCommand, resumeSessionID)

	// Stop the watcher goroutine and wait for it to finish before returning
	slog.Info("OpenCode has exited, stopping watcher")
	StopWatcher()

	// Return any execution error
	if err != nil {
		return fmt.Errorf("opencode execution failed: %w", err)
	}

	return nil
}

// WatchAgent watches for OpenCode agent activity and calls the callback with AgentChatSession.
func (p *Provider) WatchAgent(ctx context.Context, projectPath string, debugRaw bool, sessionCallback func(*spi.AgentChatSession)) error {
	slog.Info("WatchAgent: Starting OpenCode activity monitoring",
		"projectPath", projectPath,
		"debugRaw", debugRaw)

	// The watcher callback directly passes AgentChatSession
	wrappedCallback := func(agentChatSession *spi.AgentChatSession) {
		slog.Debug("WatchAgent: Received AgentChatSession",
			"sessionID", agentChatSession.SessionID,
			"hasSessionData", agentChatSession.SessionData != nil)

		// Call the user's callback with the AgentChatSession
		sessionCallback(agentChatSession)
	}

	// Set up the callback for the watcher
	SetWatcherCallback(wrappedCallback)
	defer ClearWatcherCallback()

	// Set debug raw mode for the watcher
	SetWatcherDebugRaw(debugRaw)

	// Start watching for OpenCode sessions in the background
	slog.Info("WatchAgent: Initializing OpenCode session monitoring...")

	if err := WatchForOpenCodeSessions(projectPath, ""); err != nil {
		slog.Error("WatchAgent: Failed to start OpenCode session watcher", "error", err)
		return fmt.Errorf("failed to start watcher: %w", err)
	}

	// Block until context is cancelled
	slog.Info("WatchAgent: Watcher started, blocking until context cancelled")

	// Wait for context cancellation
	<-ctx.Done()

	slog.Info("WatchAgent: Context cancelled, stopping watcher")
	StopWatcher()

	return ctx.Err()
}

// ListAgentChatSessions retrieves lightweight session metadata without full parsing.
func (p *Provider) ListAgentChatSessions(projectPath string) ([]spi.SessionMetadata, error) {
	// Find all sessions for this project
	sessions, err := findOpenCodeSessions(projectPath, "", false)
	if err != nil {
		// If database doesn't exist, return empty list (not an error)
		if strings.Contains(err.Error(), "database not found") {
			return []spi.SessionMetadata{}, nil
		}
		return nil, fmt.Errorf("failed to find opencode sessions: %w", err)
	}

	// Open database for reading
	dbPath, err := resolveDBPath()
	if err != nil {
		return nil, err
	}
	db, err := openDBReadOnly(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Extract metadata from each session
	result := make([]spi.SessionMetadata, 0, len(sessions))
	for _, sessionInfo := range sessions {
		metadata, err := extractSessionMetadata(db, &sessionInfo)
		if err != nil {
			slog.Warn("Failed to extract session metadata",
				"sessionID", sessionInfo.SessionID,
				"error", err)
			continue
		}

		// Skip empty sessions
		if metadata == nil {
			slog.Debug("Skipping empty session", "sessionID", sessionInfo.SessionID)
			continue
		}

		result = append(result, *metadata)
	}

	return result, nil
}

// processSessionToAgentChat converts an openCodeSessionInfo into an AgentChatSession.
func processSessionToAgentChat(db *sql.DB, sessionInfo *openCodeSessionInfo, workspaceRoot string, debugRaw bool) (*spi.AgentChatSession, error) {
	// Generate SessionData from database
	sessionData, err := GenerateAgentSession(db, sessionInfo, workspaceRoot)
	if err != nil {
		slog.Error("Failed to generate SessionData", "sessionID", sessionInfo.SessionID, "error", err)
		return nil, fmt.Errorf("failed to generate SessionData: %w", err)
	}

	// Skip empty sessions
	if sessionData == nil {
		return nil, nil
	}

	// Get first user message for slug generation
	firstUserMsg := extractFirstUserMessageFromDB(db, sessionInfo.SessionID)
	slug := deriveSlug(sessionInfo, firstUserMsg)
	if slug == "" {
		slog.Debug("processSessionToAgentChat: No user message for slug yet",
			"sessionID", sessionInfo.SessionID)
	}

	// Convert timestamp
	createdAt := convertMsToISO8601(sessionInfo.TimeCreated)

	// Write provider-specific debug files if requested
	if debugRaw {
		if err := writeDebugRawFiles(sessionInfo.SessionID, sessionData); err != nil {
			slog.Debug("processSessionToAgentChat: Failed to write debug files",
				"sessionID", sessionInfo.SessionID,
				"error", err)
		}
	}

	return &spi.AgentChatSession{
		SessionID:   sessionInfo.SessionID,
		CreatedAt:   createdAt,
		Slug:        slug,
		SessionData: sessionData,
		RawData:     "", // Raw data is in SQLite, not easily serialized
	}, nil
}

// extractSessionMetadata reads minimal data from an OpenCode session to extract metadata.
func extractSessionMetadata(db *sql.DB, sessionInfo *openCodeSessionInfo) (*spi.SessionMetadata, error) {
	// Get first user message for slug and name
	firstUserMsg := extractFirstUserMessageFromDB(db, sessionInfo.SessionID)
	if firstUserMsg == "" && sessionInfo.Title == "" {
		return nil, nil // Empty session
	}

	// Use title if no user message
	displayText := firstUserMsg
	if displayText == "" {
		displayText = sessionInfo.Title
	}

	// Generate slug
	slug := deriveSlug(sessionInfo, displayText)

	// Generate human-readable name
	name := spi.GenerateReadableName(displayText)

	// Convert timestamp
	createdAt := convertMsToISO8601(sessionInfo.TimeCreated)

	return &spi.SessionMetadata{
		SessionID: sessionInfo.SessionID,
		CreatedAt: createdAt,
		Slug:      slug,
		Name:      name,
	}, nil
}

// writeDebugRawFiles writes debug JSON files for an OpenCode session.
func writeDebugRawFiles(sessionID string, sessionData *SessionData) error {
	// Get the debug directory path
	debugDir := spi.GetDebugDir(sessionID)

	// Create the debug directory
	if err := os.MkdirAll(debugDir, 0755); err != nil {
		return fmt.Errorf("failed to create debug directory: %w", err)
	}

	// Write session data as JSON
	sessionJSON, err := json.MarshalIndent(sessionData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session data: %w", err)
	}

	debugPath := filepath.Join(debugDir, "session-data.json")
	if err := os.WriteFile(debugPath, sessionJSON, 0644); err != nil {
		return fmt.Errorf("failed to write debug file: %w", err)
	}

	slog.Debug("writeDebugRawFiles: Wrote debug file", "path", debugPath)

	return nil
}
