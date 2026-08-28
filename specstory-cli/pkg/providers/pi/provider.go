package pi

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi"
)

// Provider implements SpecStory's Provider SPI for the Pi coding agent.
type Provider struct{}

func NewProvider() *Provider { return &Provider{} }

func (p *Provider) Name() string { return "Pi" }

func parsePiCommand(customCommand string) (string, []string) {
	if strings.TrimSpace(customCommand) != "" {
		parts := spi.SplitCommandLine(customCommand)
		if len(parts) > 0 {
			return parts[0], parts[1:]
		}
	}
	return "pi", nil
}

func (p *Provider) Check(customCommand string) spi.CheckResult {
	command, args := parsePiCommand(customCommand)
	location := command
	if !filepath.IsAbs(command) {
		if path, err := exec.LookPath(command); err == nil {
			location = path
		}
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(command, append(args, "--version")...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return spi.CheckResult{Success: false, Location: location, ErrorMessage: fmt.Sprintf("failed to run pi --version: %v. Stderr: %s", err, strings.TrimSpace(stderr.String()))}
	}
	version := strings.TrimSpace(stdout.String())
	if version == "" {
		version = "unknown"
	}
	return spi.CheckResult{Success: true, Version: version, Location: location}
}

func (p *Provider) DetectAgent(projectPath string, helpOutput bool) bool {
	sessions, err := findPiSessions(projectPath, "")
	return err == nil && len(sessions) > 0
}

func (p *Provider) GetAgentChatSession(projectPath string, sessionID string, debugRaw bool) (*spi.AgentChatSession, error) {
	sessions, err := findPiSessions(projectPath, sessionID)
	if err != nil || len(sessions) == 0 {
		return nil, err
	}
	return parsePiSession(sessions[0].Path, projectPath)
}

func (p *Provider) GetAgentChatSessions(projectPath string, debugRaw bool, progress spi.ProgressCallback) ([]spi.AgentChatSession, error) {
	sessions, err := findPiSessions(projectPath, "")
	if err != nil {
		return nil, err
	}
	result := make([]spi.AgentChatSession, 0, len(sessions))
	for i, session := range sessions {
		parsed, err := parsePiSession(session.Path, projectPath)
		if err == nil && parsed != nil {
			result = append(result, *parsed)
		}
		if progress != nil {
			progress(i+1, len(sessions))
		}
	}
	return result, nil
}

func (p *Provider) ListAgentChatSessions(projectPath string) ([]spi.SessionMetadata, error) {
	sessions, err := findPiSessions(projectPath, "")
	if err != nil {
		return nil, err
	}
	result := make([]spi.SessionMetadata, 0, len(sessions))
	for _, session := range sessions {
		parsed, err := parsePiSession(session.Path, projectPath)
		if err != nil || parsed == nil {
			continue
		}
		result = append(result, spi.SessionMetadata{SessionID: session.ID, CreatedAt: session.CreatedAt, Slug: parsed.Slug, Name: parsed.Slug})
	}
	return result, nil
}

func (p *Provider) ExecAgentAndWatch(projectPath string, customCommand string, resumeSessionID string, debugRaw bool, sessionCallback func(*spi.AgentChatSession)) error {
	command, args := parsePiCommand(customCommand)
	if resumeSessionID != "" {
		sessions, err := findPiSessions(projectPath, resumeSessionID)
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			return fmt.Errorf("pi session %q not found for %s", resumeSessionID, projectPath)
		}
		args = append(args, "--session", sessions[0].Path)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watcher := NewWatcher(projectPath, debugRaw, sessionCallback)
	go func() {
		if err := watcher.Watch(ctx); err != nil {
			slog.Error("Pi watcher stopped", "error", err)
		}
	}()
	cmd := exec.Command(command, args...)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = projectPath, os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pi execution failed: %w", err)
	}
	return nil
}

func (p *Provider) WatchAgent(ctx context.Context, projectPath string, debugRaw bool, sessionCallback func(*spi.AgentChatSession)) error {
	return NewWatcher(projectPath, debugRaw, sessionCallback).Watch(ctx)
}
