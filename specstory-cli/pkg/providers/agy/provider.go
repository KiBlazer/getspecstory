package agy

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi"
)

// Provider implements the spi.Provider interface for Antigravity (agy)
type Provider struct{}

// NewProvider creates a new Provider instance
func NewProvider() *Provider {
	return &Provider{}
}

// Name returns the human-readable name of this provider
func (p *Provider) Name() string {
	return "Antigravity"
}

func parseAgyCommand(customCommand string) (string, []string) {
	if customCommand != "" {
		parts := spi.SplitCommandLine(customCommand)
		if len(parts) > 0 {
			return parts[0], parts[1:]
		}
	}
	return "agy", nil
}

// Check verifies the installation and version of agy CLI
func (p *Provider) Check(customCommand string) spi.CheckResult {
	agyCmd, args := parseAgyCommand(customCommand)
	isCustom := customCommand != ""

	resolvedPath := agyCmd
	if !filepath.IsAbs(agyCmd) {
		if path, err := exec.LookPath(agyCmd); err == nil {
			resolvedPath = path
		}
	}

	cmdArgs := append(args, "--version")
	cmd := exec.Command(agyCmd, cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errorMessage := fmt.Sprintf("failed to run agy --version: %v. Stderr: %s", err, stderr.String())
		slog.Warn("Agy check failed", "path", resolvedPath, "isCustom", isCustom, "error", errorMessage)
		return spi.CheckResult{
			Success:      false,
			Location:     resolvedPath,
			ErrorMessage: errorMessage,
		}
	}

	version := strings.TrimSpace(stdout.String())
	if version == "" {
		version = "unknown"
	}

	return spi.CheckResult{
		Success:  true,
		Version:  version,
		Location: resolvedPath,
	}
}

// DetectAgent determines if there are agy sessions associated with the project path
func (p *Provider) DetectAgent(projectPath string, helpOutput bool) bool {
	lines, err := readHistoryLines()
	if err == nil {
		normProjPath := normalizePath(projectPath)
		for _, line := range lines {
			if normalizePath(line.Workspace) == normProjPath {
				return true
			}
		}
	}

	if lastConvs, err := readLastConversations(); err == nil {
		for ws, convID := range lastConvs {
			if normalizePath(ws) == normalizePath(projectPath) && convID != "" {
				return true
			}
		}
	}

	return false
}

// GetAgentChatSession parses and returns a single conversation session
func (p *Provider) GetAgentChatSession(projectPath string, sessionID string, debugRaw bool) (*spi.AgentChatSession, error) {
	sessionData, err := ParseTranscript(sessionID, projectPath)
	if err != nil {
		return nil, err
	}

	brainDir, err := getBrainDir()
	if err != nil {
		return nil, err
	}
	transcriptPath := filepath.Join(brainDir, sessionID, ".system_generated", "logs", "transcript.jsonl")
	rawBytes, _ := os.ReadFile(transcriptPath)

	return &spi.AgentChatSession{
		SessionID:   sessionID,
		CreatedAt:   sessionData.CreatedAt,
		Slug:        sessionID,
		SessionData: sessionData,
		RawData:     string(rawBytes),
	}, nil
}

// GetAgentChatSessions parses and returns all conversation sessions for the project path
func (p *Provider) GetAgentChatSessions(projectPath string, debugRaw bool, progress spi.ProgressCallback) ([]spi.AgentChatSession, error) {
	lines, err := readHistoryLines()
	if err != nil {
		return nil, err
	}

	normProjPath := normalizePath(projectPath)
	var sessionIDs []string
	seen := make(map[string]bool)

	for _, line := range lines {
		if line.ConversationID == "" {
			continue
		}
		if normalizePath(line.Workspace) == normProjPath {
			if !seen[line.ConversationID] {
				seen[line.ConversationID] = true
				sessionIDs = append(sessionIDs, line.ConversationID)
			}
		}
	}

	if lastConvs, err := readLastConversations(); err == nil {
		for ws, convID := range lastConvs {
			if normalizePath(ws) == normProjPath && convID != "" {
				if !seen[convID] {
					seen[convID] = true
					sessionIDs = append(sessionIDs, convID)
				}
			}
		}
	}

	total := len(sessionIDs)
	var sessions []spi.AgentChatSession
	for i, id := range sessionIDs {
		sess, err := p.GetAgentChatSession(projectPath, id, debugRaw)
		if err == nil && sess != nil {
			sessions = append(sessions, *sess)
		}
		if progress != nil {
			progress(i+1, total)
		}
	}

	return sessions, nil
}

// ListAgentChatSessions retrieves lightweight metadata for all conversation sessions for the project path
func (p *Provider) ListAgentChatSessions(projectPath string) ([]spi.SessionMetadata, error) {
	lines, err := readHistoryLines()
	if err != nil {
		return nil, err
	}

	normProjPath := normalizePath(projectPath)
	var list []spi.SessionMetadata
	seen := make(map[string]bool)

	for _, line := range lines {
		if line.ConversationID == "" {
			continue
		}
		if normalizePath(line.Workspace) == normProjPath {
			if !seen[line.ConversationID] {
				seen[line.ConversationID] = true

				t := time.Unix(line.Timestamp/1000, 0)
				createdAt := t.Format(time.RFC3339)

				list = append(list, spi.SessionMetadata{
					SessionID: line.ConversationID,
					CreatedAt: createdAt,
					Slug:      line.ConversationID,
					Name:      line.Display,
				})
			}
		}
	}

	if lastConvs, err := readLastConversations(); err == nil {
		for ws, convID := range lastConvs {
			if normalizePath(ws) == normProjPath && convID != "" {
				if !seen[convID] {
					seen[convID] = true
					brainDir, _ := getBrainDir()
					createdAt := time.Now().Format(time.RFC3339)
					name := "Active Session"

					logDir := filepath.Join(brainDir, convID, ".system_generated", "logs")
					if info, err := os.Stat(logDir); err == nil {
						createdAt = info.ModTime().Format(time.RFC3339)
					}

					list = append(list, spi.SessionMetadata{
						SessionID: convID,
						CreatedAt: createdAt,
						Slug:      convID,
						Name:      name,
					})
				}
			}
		}
	}

	return list, nil
}

// ExecAgentAndWatch executes agy interactive loop and watches session files
func (p *Provider) ExecAgentAndWatch(projectPath string, customCommand string, resumeSessionID string, debugRaw bool, sessionCallback func(*spi.AgentChatSession)) error {
	agyCmd, args := parseAgyCommand(customCommand)

	if resumeSessionID != "" {
		args = append(args, "--conversation", resumeSessionID)
	}

	cmd := exec.Command(agyCmd, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := NewWatcher(projectPath, debugRaw, sessionCallback)
	go func() {
		if err := watcher.Watch(ctx); err != nil {
			slog.Error("Agy watcher background run failed", "error", err)
		}
	}()

	slog.Info("ExecAgentAndWatch: Starting Antigravity CLI", "command", agyCmd, "args", args)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("agy execution failed: %w", err)
	}

	return nil
}

// WatchAgent monitors agy logs without starting the interactive agent
func (p *Provider) WatchAgent(ctx context.Context, projectPath string, debugRaw bool, sessionCallback func(*spi.AgentChatSession)) error {
	watcher := NewWatcher(projectPath, debugRaw, sessionCallback)
	return watcher.Watch(ctx)
}
