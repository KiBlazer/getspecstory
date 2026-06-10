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


	agyDir, err := getAgyDir()
	if err != nil {
		return err
	}

	brainDir, err := getBrainDir()
	if err != nil {
		return err
	}

	cacheDir, err := getCacheDir()
	if err != nil {
		return err
	}

	// Ensure directories exist
	os.MkdirAll(agyDir, 0755)
	os.MkdirAll(brainDir, 0755)
	os.MkdirAll(cacheDir, 0755)

	// Watch the parent agyDir (to detect history.jsonl updates robustly against atomic renames)
	if err := watcher.Add(agyDir); err != nil {
		return fmt.Errorf("failed to watch agy directory: %w", err)
	}

	// Watch the brain directory (to detect new conversation folders dynamically)
	if err := watcher.Add(brainDir); err != nil {
		return fmt.Errorf("failed to watch brain directory: %w", err)
	}

	// Watch the cache directory (to detect last_conversations.json updates)
	if err := watcher.Add(cacheDir); err != nil {
		return fmt.Errorf("failed to watch cache directory: %w", err)
	}

	// Watch existing transcripts of this workspace
	w.watchExistingTranscripts(watcher, brainDir)

	slog.Info("Watching agy directory, brain directory, and cache directory", "agyDir", agyDir, "brainDir", brainDir, "cacheDir", cacheDir)

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

			// If history.jsonl is updated (written to, created, or renamed/replaced)
			if filepath.Base(event.Name) == "history.jsonl" && (event.Has(fsnotify.Write) || event.Has(fsnotify.Create)) {
				w.handleHistoryUpdate(watcher, brainDir)
			}

			// If last_conversations.json is updated (written to, created, or renamed/replaced)
			if filepath.Base(event.Name) == "last_conversations.json" && (event.Has(fsnotify.Write) || event.Has(fsnotify.Create)) {
				w.handleLastConversationsUpdate(watcher, brainDir)
			}

			// If a new conversation folder is created under brain/
			if filepath.Dir(event.Name) == brainDir && event.Has(fsnotify.Create) {
				convID := filepath.Base(event.Name)
				if len(convID) == 36 { // Check for UUID structure
					logDir := filepath.Join(event.Name, ".system_generated", "logs")
					go w.waitAndAddWatch(watcher, logDir, convID)
				}
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

func (w *Watcher) waitAndAddWatch(watcher *fsnotify.Watcher, logDir string, convID string) {
	// Wait up to 5 seconds for the .system_generated/logs directory to be created by agy
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(logDir); err == nil {
			w.mu.Lock()
			w.watchedIDs[convID] = true
			w.mu.Unlock()
			watcher.Add(logDir)
			slog.Info("Agy watcher: dynamically added watch for new log directory", "logDir", logDir)

			// Fire initial callback to capture session start
			go w.fireSessionCallback(convID)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	slog.Warn("Agy watcher: timed out waiting for log directory", "logDir", logDir)
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

	// Also check last_conversations.json for the active session of this workspace
	if lastConvs, err := readLastConversations(); err == nil {
		for ws, convID := range lastConvs {
			if normalizePath(ws) == w.projectPath && convID != "" {
				w.watchedIDs[convID] = true
				logDir := filepath.Join(brainDir, convID, ".system_generated", "logs")
				transcriptPath := filepath.Join(logDir, "transcript.jsonl")
				if _, err := os.Stat(transcriptPath); err == nil {
					watcher.Add(logDir)
				}
			}
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

func (w *Watcher) handleLastConversationsUpdate(watcher *fsnotify.Watcher, brainDir string) {
	lastConvs, err := readLastConversations()
	if err != nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	for ws, convID := range lastConvs {
		if normalizePath(ws) != w.projectPath || convID == "" {
			continue
		}

		if !w.watchedIDs[convID] {
			w.watchedIDs[convID] = true
			logDir := filepath.Join(brainDir, convID, ".system_generated", "logs")
			go w.waitAndAddWatch(watcher, logDir, convID)
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
