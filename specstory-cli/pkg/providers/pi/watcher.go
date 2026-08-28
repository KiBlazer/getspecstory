package pi

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

// Watcher observes Pi's JSONL session tree. fsnotify is not recursive, so every
// existing directory and every newly created directory is explicitly registered.
type Watcher struct {
	projectPath string
	debugRaw    bool
	callback    func(*spi.AgentChatSession)
	mu          sync.Mutex
	generation  map[string]uint64
}

func NewWatcher(projectPath string, debugRaw bool, callback func(*spi.AgentChatSession)) *Watcher {
	return &Watcher{projectPath: projectPath, debugRaw: debugRaw, callback: callback, generation: make(map[string]uint64)}
}

func (w *Watcher) Watch(ctx context.Context) error {
	root, err := piSessionsRoot()
	if err != nil {
		return err
	}
	// This allows `specstory run pi` to observe Pi's very first persisted session.
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("pi: create sessions directory: %w", err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	watched := make(map[string]bool)
	add := func(dir string) {
		if watched[dir] {
			return
		}
		if err := watcher.Add(dir); err != nil {
			slog.Debug("Pi watcher could not watch directory", "path", dir, "error", err)
			return
		}
		watched[dir] = true
	}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			add(path)
		}
		return nil
	})
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					add(event.Name)
					continue
				}
			}
			if !strings.HasSuffix(event.Name, ".jsonl") || (!event.Has(fsnotify.Create) && !event.Has(fsnotify.Write)) {
				continue
			}
			w.fire(event.Name)
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			slog.Debug("Pi watcher event error", "error", err)
		}
	}
}

func (w *Watcher) fire(path string) {
	w.mu.Lock()
	w.generation[path]++
	generation := w.generation[path]
	w.mu.Unlock()
	go func() {
		// Delay until writes have settled. A generation counter makes this a
		// trailing debounce: only the final event for a path reaches the parser.
		time.Sleep(300 * time.Millisecond)
		w.mu.Lock()
		if w.generation[path] != generation {
			w.mu.Unlock()
			return
		}
		delete(w.generation, path)
		w.mu.Unlock()
		// Parse with the header's workspace rather than the watched project so a
		// session created for another project cannot be reported to this watcher.
		session, err := parsePiSession(path, "")
		if err != nil || session == nil || normalizePiPath(session.SessionData.WorkspaceRoot) != normalizePiPath(w.projectPath) {
			return
		}
		w.callback(session)
	}()
}
