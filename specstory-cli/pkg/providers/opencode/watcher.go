package opencode

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/specstoryai/getspecstory/specstory-cli/pkg/log"
	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi"
)

var (
	watcherCtx      context.Context
	watcherCancel   context.CancelFunc
	watcherWg       sync.WaitGroup
	watcherCallback func(*spi.AgentChatSession) // Callback for session updates
	watcherDebugRaw bool                        // Whether to write debug raw data files
	watcherMutex    sync.RWMutex                // Protects watcherCallback and watcherDebugRaw
)

// resetWatcherContext creates a fresh context for the watcher.
// This must be called before starting a new watcher to ensure the previous
// cancelled context doesn't cause the new watcher to exit immediately.
func resetWatcherContext() {
	if watcherCancel != nil {
		watcherCancel()
	}
	watcherCtx, watcherCancel = context.WithCancel(context.Background())
}

// SetWatcherCallback sets the callback function for session updates.
func SetWatcherCallback(callback func(*spi.AgentChatSession)) {
	watcherMutex.Lock()
	defer watcherMutex.Unlock()
	watcherCallback = callback
	slog.Info("SetWatcherCallback: Callback set", "isNil", callback == nil)
}

// ClearWatcherCallback clears the callback function.
func ClearWatcherCallback() {
	watcherMutex.Lock()
	defer watcherMutex.Unlock()
	watcherCallback = nil
	slog.Info("ClearWatcherCallback: Callback cleared")
}

// getWatcherCallback returns the current callback function (thread-safe).
func getWatcherCallback() func(*spi.AgentChatSession) {
	watcherMutex.RLock()
	defer watcherMutex.RUnlock()
	return watcherCallback
}

// SetWatcherDebugRaw sets whether to write debug raw data files.
func SetWatcherDebugRaw(debugRaw bool) {
	watcherMutex.Lock()
	defer watcherMutex.Unlock()
	watcherDebugRaw = debugRaw
	slog.Debug("SetWatcherDebugRaw: Debug raw set", "debugRaw", debugRaw)
}

// getWatcherDebugRaw returns the current debug raw setting (thread-safe).
func getWatcherDebugRaw() bool {
	watcherMutex.RLock()
	defer watcherMutex.RUnlock()
	return watcherDebugRaw
}

// StopWatcher gracefully stops the watcher goroutine.
func StopWatcher() {
	slog.Info("StopWatcher: Signaling watcher to stop")
	watcherCancel()
	slog.Info("StopWatcher: Waiting for watcher goroutine to finish")
	watcherWg.Wait()
	slog.Info("StopWatcher: Watcher stopped")
}

// WatchForOpenCodeSessions watches for OpenCode sessions that match the given project path.
// Uses a hybrid approach: SQLite polling + JSON file watching.
func WatchForOpenCodeSessions(projectPath string, resumeSessionID string) error {
	slog.Info("WatchForOpenCodeSessions: Starting OpenCode session watcher",
		"projectPath", projectPath,
		"resumeSessionID", resumeSessionID)

	// Create a fresh context for this watcher instance
	resetWatcherContext()

	// Get the database path
	dbPath, err := resolveDBPath()
	if err != nil {
		slog.Error("WatchForOpenCodeSessions: Failed to resolve database path", "error", err)
		return fmt.Errorf("failed to resolve database path: %w", err)
	}

	slog.Info("WatchForOpenCodeSessions: Database path", "path", dbPath)

	// Get the storage session directory for file watching
	homeDir, err := osUserHomeDir()
	if err != nil {
		slog.Error("WatchForOpenCodeSessions: Failed to get home directory", "error", err)
		return fmt.Errorf("failed to get home directory: %w", err)
	}
	storageSessionDir := filepath.Join(homeDir, ".local", "share", "opencode", "storage", "session")

	return startOpenCodeSessionWatcher(projectPath, dbPath, storageSessionDir)
}

// startOpenCodeSessionWatcher starts watching for OpenCode sessions using hybrid approach.
func startOpenCodeSessionWatcher(projectPath string, dbPath string, storageSessionDir string) error {
	slog.Info("startOpenCodeSessionWatcher: Creating hybrid watcher",
		"dbPath", dbPath,
		"storageSessionDir", storageSessionDir)

	// Create a new watcher for JSON files
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Warn("startOpenCodeSessionWatcher: Failed to create file watcher, falling back to polling only", "error", err)
		// Continue with polling only
		watcher = nil
	}

	// Increment wait group before starting goroutine
	watcherWg.Add(1)

	// Start watching in a goroutine
	go func() {
		// Decrement wait group when done
		defer watcherWg.Done()

		// Log when goroutine starts
		slog.Info("startOpenCodeSessionWatcher: Goroutine started")

		// Defer cleanup
		defer func() {
			slog.Info("startOpenCodeSessionWatcher: Goroutine exiting")
			if watcher != nil {
				if err := watcher.Close(); err != nil {
					slog.Debug("startOpenCodeSessionWatcher: Error closing watcher", "error", err)
				}
			}
		}()

		// Track last seen session updates
		lastSeenTime := make(map[string]int64)
		var lastSeenMutex sync.Mutex

		// Initial scan of existing sessions
		initialSessions, err := findOpenCodeSessions(projectPath, "", false)
		if err != nil {
			slog.Debug("startOpenCodeSessionWatcher: Initial scan failed", "error", err)
		} else {
			lastSeenMutex.Lock()
			for _, s := range initialSessions {
				lastSeenTime[s.SessionID] = s.TimeUpdated
			}
			lastSeenMutex.Unlock()
			slog.Info("startOpenCodeSessionWatcher: Initial scan complete", "sessions", len(initialSessions))
		}

		// Add storage session directory to watcher if available
		if watcher != nil {
			if info, err := os.Stat(storageSessionDir); err == nil && info.IsDir() {
				if err := watcher.Add(storageSessionDir); err != nil {
					slog.Warn("startOpenCodeSessionWatcher: Failed to watch storage directory", "error", err)
				} else {
					slog.Info("startOpenCodeSessionWatcher: Watching storage directory", "path", storageSessionDir)
				}
				// Also watch subdirectories
				entries, err := os.ReadDir(storageSessionDir)
				if err == nil {
					for _, entry := range entries {
						if entry.IsDir() {
							subdir := filepath.Join(storageSessionDir, entry.Name())
							if err := watcher.Add(subdir); err != nil {
								slog.Debug("startOpenCodeSessionWatcher: Failed to watch subdirectory", "path", subdir, "error", err)
							}
						}
					}
				}
			}
		}

		// Create ticker for periodic SQLite polling (every 5 seconds)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		// Watch for events
		slog.Info("startOpenCodeSessionWatcher: Now watching for events")
		for {
			select {
			case <-watcherCtx.Done():
				slog.Info("startOpenCodeSessionWatcher: Context cancelled, stopping watcher")
				return

			case event, ok := <-watcher.Events:
				if !ok || watcher == nil {
					continue
				}

				if !event.Has(fsnotify.Create) && !event.Has(fsnotify.Write) {
					continue
				}

				// Check if this is a JSON file
				if strings.HasSuffix(event.Name, ".json") {
					slog.Info("startOpenCodeSessionWatcher: JSON file event",
						"operation", event.Op.String(),
						"file", event.Name)

					// Trigger a scan for updated sessions
					scanForUpdatedSessions(projectPath, dbPath, lastSeenTime, &lastSeenMutex)
				}

			case <-ticker.C:
				// Periodic SQLite polling
				scanForUpdatedSessions(projectPath, dbPath, lastSeenTime, &lastSeenMutex)

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.UserWarn("Watcher error: %v", err)
				slog.Error("startOpenCodeSessionWatcher: Watcher error", "error", err)
			}
		}
	}()

	return nil
}

// scanForUpdatedSessions checks for new or updated sessions via SQLite polling.
func scanForUpdatedSessions(projectPath string, dbPath string, lastSeenTime map[string]int64, lastSeenMutex *sync.Mutex) {
	callback := getWatcherCallback()
	if callback == nil {
		return
	}

	// Get the maximum last seen time
	lastSeenMutex.Lock()
	var maxLastSeen int64
	for _, t := range lastSeenTime {
		if t > maxLastSeen {
			maxLastSeen = t
		}
	}
	lastSeenMutex.Unlock()

	// Open database connection for this scan
	db, err := openDBReadOnly(dbPath)
	if err != nil {
		slog.Debug("scanForUpdatedSessions: Failed to open database", "error", err)
		return
	}
	defer db.Close()

	// Query for updated sessions using the shared connection
	updatedSessions, err := findOpenCodeSessionsUpdatedSince(db, projectPath, maxLastSeen)
	if err != nil {
		slog.Debug("scanForUpdatedSessions: Failed to query updated sessions", "error", err)
		return
	}

	for _, sessionInfo := range updatedSessions {
		lastSeenMutex.Lock()
		prevTime, exists := lastSeenTime[sessionInfo.SessionID]
		lastSeenMutex.Unlock()

		// Skip if we've already seen this update
		if exists && prevTime >= sessionInfo.TimeUpdated {
			continue
		}

		slog.Info("scanForUpdatedSessions: New/updated session detected",
			"sessionID", sessionInfo.SessionID,
			"timeUpdated", sessionInfo.TimeUpdated)

		// Update last seen time
		lastSeenMutex.Lock()
		lastSeenTime[sessionInfo.SessionID] = sessionInfo.TimeUpdated
		lastSeenMutex.Unlock()

		// Process the session with the shared database connection
		processOpenCodeSessionUpdate(db, &sessionInfo, projectPath, callback)
	}
}

// processOpenCodeSessionUpdate processes a single OpenCode session update.
func processOpenCodeSessionUpdate(db *sql.DB, sessionInfo *openCodeSessionInfo, projectPath string, callback func(*spi.AgentChatSession)) {
	// Process the session using the provided database connection
	agentSession, err := processSessionToAgentChat(db, sessionInfo, projectPath, getWatcherDebugRaw())
	if err != nil {
		slog.Debug("processOpenCodeSessionUpdate: Failed to process session",
			"sessionID", sessionInfo.SessionID,
			"error", err)
		return
	}

	// Skip empty sessions
	if agentSession == nil {
		slog.Debug("processOpenCodeSessionUpdate: Skipping empty session", "sessionID", sessionInfo.SessionID)
		return
	}

	// Skip sessions without user message (empty slug)
	if agentSession.Slug == "" {
		slog.Debug("processOpenCodeSessionUpdate: Skipping session without user message",
			"sessionID", agentSession.SessionID)
		return
	}

	slog.Info("processOpenCodeSessionUpdate: Calling callback for session", "sessionID", agentSession.SessionID)
	// Call the callback in a goroutine to avoid blocking
	go func(s *spi.AgentChatSession) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("processOpenCodeSessionUpdate: Callback panicked", "panic", r)
			}
		}()
		callback(s)
	}(agentSession)
}
