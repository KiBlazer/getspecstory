package agy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi"
)

// Watcher monitors agy history.jsonl and transcript.jsonl files for updates
type Watcher struct {
	projectPath string
	callback    func(*spi.AgentChatSession)
	debugRaw    bool
	mu          sync.Mutex
	watchedIDs  map[string]bool
}

// NewWatcher creates a new Watcher instance
func NewWatcher(projectPath string, debugRaw bool, callback func(*spi.AgentChatSession)) *Watcher {
	return &Watcher{
		projectPath: normalizePath(projectPath),
		callback:    callback,
		debugRaw:    debugRaw,
		watchedIDs:  make(map[string]bool),
	}
}

// Watch starts the filesystem monitoring loop
func (w *Watcher) Watch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer watcher.Close()

	historyPath, err := getHistoryPath()
	if err != nil {
		return err
	}

	// Ensure the history file exists
	if _, err := os.Stat(historyPath); os.IsNotExist(err) {
		os.MkdirAll(filepath.Dir(historyPath), 0755)
		f, err := os.Create(historyPath)
		if err == nil {
			f.Close()
		}
	}

	// Watch history.jsonl
	if err := watcher.Add(historyPath); err != nil {
		return fmt.Errorf("failed to watch history file: %w", err)
	}

	brainDir, err := getBrainDir()
	if err != nil {
		return err
	}

	// Watch existing transcripts of this workspace
	w.watchExistingTranscripts(watcher, brainDir)

	slog.Info("Watching agy history and brain directory", "historyPath", historyPath, "brainDir", brainDir)

	// Debounce map to avoid firing multiple callbacks for same update in quick succession
	lastFired := make(map[string]time.Time)
	var debounceMu sync.Mutex

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			// If history.jsonl is written to, a new conversation might have started
			if event.Name == historyPath && event.Has(fsnotify.Write) {
				w.handleHistoryUpdate(watcher, brainDir)
			}

			// If a transcript.jsonl changes
			if strings.HasSuffix(event.Name, "transcript.jsonl") && event.Has(fsnotify.Write) {
				parts := strings.Split(filepath.ToSlash(event.Name), "/")
				if len(parts) >= 4 {
					convID := parts[len(parts)-4]

					debounceMu.Lock()
					lastTime := lastFired[convID]
					now := time.Now()
					if now.Sub(lastTime) > 300*time.Millisecond {
						lastFired[convID] = now
						debounceMu.Unlock()

						go w.fireSessionCallback(convID)
					} else {
						debounceMu.Unlock()
					}
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			slog.Error("Watcher error", "error", err)
		}
	}
}

func (w *Watcher) watchExistingTranscripts(watcher *fsnotify.Watcher, brainDir string) {
	lines, err := readHistoryLines()
	if err != nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	for _, line := range lines {
		if line.ConversationID == "" {
			continue
		}
		if normalizePath(line.Workspace) != w.projectPath {
			continue
		}

		w.watchedIDs[line.ConversationID] = true
		logDir := filepath.Join(brainDir, line.ConversationID, ".system_generated", "logs")
		transcriptPath := filepath.Join(logDir, "transcript.jsonl")
		if _, err := os.Stat(transcriptPath); err == nil {
			watcher.Add(logDir)
		}
	}
}

func (w *Watcher) handleHistoryUpdate(watcher *fsnotify.Watcher, brainDir string) {
	lines, err := readHistoryLines()
	if err != nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	for _, line := range lines {
		if line.ConversationID == "" {
			continue
		}
		if normalizePath(line.Workspace) != w.projectPath {
			continue
		}

		if !w.watchedIDs[line.ConversationID] {
			w.watchedIDs[line.ConversationID] = true
			logDir := filepath.Join(brainDir, line.ConversationID, ".system_generated", "logs")
			os.MkdirAll(logDir, 0755)
			watcher.Add(logDir)

			go w.fireSessionCallback(line.ConversationID)
		}
	}
}

func (w *Watcher) fireSessionCallback(conversationID string) {
	slog.Info("Agy watcher: firing callback for session", "conversationID", conversationID)
	sessionData, err := ParseTranscript(conversationID, w.projectPath)
	if err != nil {
		slog.Error("Agy watcher: failed to parse transcript", "conversationID", conversationID, "error", err)
		return
	}

	brainDir, _ := getBrainDir()
	transcriptPath := filepath.Join(brainDir, conversationID, ".system_generated", "logs", "transcript.jsonl")
	rawBytes, _ := os.ReadFile(transcriptPath)

	agentSession := &spi.AgentChatSession{
		SessionID:   conversationID,
		CreatedAt:   sessionData.CreatedAt,
		Slug:        sessionData.SessionID,
		SessionData: sessionData,
		RawData:     string(rawBytes),
	}

	w.callback(agentSession)
}
