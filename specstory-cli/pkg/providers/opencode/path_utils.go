package opencode

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Package-level variables for mocking in tests
var (
	osUserHomeDir = os.UserHomeDir
	osStat        = os.Stat
)

// openCodeSessionInfo holds information about a discovered OpenCode session.
type openCodeSessionInfo struct {
	SessionID   string
	Directory   string
	Title       string
	Slug        string
	Version     string
	TimeCreated int64
	TimeUpdated int64
}

// resolveDBPath returns the path to the OpenCode SQLite database.
// Location: ~/.local/share/opencode/opencode.db
func resolveDBPath() (string, error) {
	home, err := osUserHomeDir()
	if err != nil {
		return "", fmt.Errorf("opencode: cannot resolve home dir: %w", err)
	}
	dbPath := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if _, err := osStat(dbPath); os.IsNotExist(err) {
		return "", fmt.Errorf("opencode: database not found at %s", dbPath)
	}
	return dbPath, nil
}

// openDBReadOnly opens the SQLite database in read-only mode with WAL journaling.
// This ensures we don't conflict with a running OpenCode process.
func openDBReadOnly(dbPath string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_journal_mode=WAL&_timeout=5000", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opencode: failed to open database: %w", err)
	}

	// Verify the connection is working
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("opencode: failed to ping database: %w", err)
	}

	return db, nil
}

// normalizeOpenCodePath resolves a path to an absolute, cleaned representation suitable for comparisons.
func normalizeOpenCodePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}

	cleaned := filepath.Clean(path)

	absPath, err := filepath.Abs(cleaned)
	if err == nil {
		cleaned = absPath
	}

	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		cleaned = resolved
	}

	return filepath.Clean(cleaned)
}

// sessionMatchesProject checks if a session's directory matches the given project path.
func sessionMatchesProject(sessionDir string, projectPath string) bool {
	if sessionDir == projectPath {
		return true
	}
	normalizedSession := normalizeOpenCodePath(sessionDir)
	normalizedProject := normalizeOpenCodePath(projectPath)
	return normalizedSession == normalizedProject
}

// findOpenCodeSessions queries the SQLite database for sessions matching the project path.
// If targetSessionID is provided, returns only that session.
// If stopOnFirst is true, returns after finding the first match.
func findOpenCodeSessions(projectPath string, targetSessionID string, stopOnFirst bool) ([]openCodeSessionInfo, error) {
	dbPath, err := resolveDBPath()
	if err != nil {
		return nil, err
	}

	db, err := openDBReadOnly(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	normalizedProjectPath := normalizeOpenCodePath(projectPath)

	var sessions []openCodeSessionInfo

	if targetSessionID != "" {
		// Query for a specific session
		rows, err := db.Query(
			"SELECT id, directory, title, slug, version, time_created, time_updated FROM session WHERE id = ?",
			targetSessionID,
		)
		if err != nil {
			return nil, fmt.Errorf("opencode: failed to query session: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var s openCodeSessionInfo
			if err := rows.Scan(&s.SessionID, &s.Directory, &s.Title, &s.Slug, &s.Version, &s.TimeCreated, &s.TimeUpdated); err != nil {
				slog.Debug("findOpenCodeSessions: Failed to scan row", "error", err)
				continue
			}
			// Check if session matches project path (normalizedProjectPath is already normalized)
			if sessionMatchesProject(s.Directory, normalizedProjectPath) {
				sessions = append(sessions, s)
				return sessions, nil // Found the specific session
			}
		}
		return sessions, nil
	}

	// Query for all sessions matching the project path
	rows, err := db.Query(
		"SELECT id, directory, title, slug, version, time_created, time_updated FROM session WHERE directory = ? ORDER BY time_created DESC",
		normalizedProjectPath,
	)
	if err != nil {
		// Try with original path as fallback
		slog.Debug("findOpenCodeSessions: Query with normalized path failed, trying original", "error", err)
		rows, err = db.Query(
			"SELECT id, directory, title, slug, version, time_created, time_updated FROM session WHERE directory = ? ORDER BY time_created DESC",
			projectPath,
		)
		if err != nil {
			return nil, fmt.Errorf("opencode: failed to query sessions: %w", err)
		}
	}
	defer rows.Close()

	for rows.Next() {
		var s openCodeSessionInfo
		if err := rows.Scan(&s.SessionID, &s.Directory, &s.Title, &s.Slug, &s.Version, &s.TimeCreated, &s.TimeUpdated); err != nil {
			slog.Debug("findOpenCodeSessions: Failed to scan row", "error", err)
			continue
		}
		sessions = append(sessions, s)
		if stopOnFirst && len(sessions) > 0 {
			return sessions, nil
		}
	}

	// If no sessions found with normalized path, try with original path
	if len(sessions) == 0 && normalizedProjectPath != projectPath {
		rows2, err := db.Query(
			"SELECT id, directory, title, slug, version, time_created, time_updated FROM session WHERE directory = ? ORDER BY time_created DESC",
			projectPath,
		)
		if err != nil {
			return sessions, nil // Return empty, not an error
		}
		defer rows2.Close()

		for rows2.Next() {
			var s openCodeSessionInfo
			if err := rows2.Scan(&s.SessionID, &s.Directory, &s.Title, &s.Slug, &s.Version, &s.TimeCreated, &s.TimeUpdated); err != nil {
				continue
			}
			sessions = append(sessions, s)
			if stopOnFirst && len(sessions) > 0 {
				return sessions, nil
			}
		}
	}

	return sessions, nil
}

// findOpenCodeSessionsUpdatedSince queries for sessions updated after the given timestamp.
// Used by the watcher to detect new/updated sessions.
func findOpenCodeSessionsUpdatedSince(db *sql.DB, projectPath string, sinceTimestamp int64) ([]openCodeSessionInfo, error) {
	normalizedProjectPath := normalizeOpenCodePath(projectPath)

	rows, err := db.Query(
		"SELECT id, directory, title, slug, version, time_created, time_updated FROM session WHERE directory = ? AND time_updated > ? ORDER BY time_updated ASC",
		normalizedProjectPath,
		sinceTimestamp,
	)
	if err != nil {
		// Try with original path
		rows, err = db.Query(
			"SELECT id, directory, title, slug, version, time_created, time_updated FROM session WHERE directory = ? AND time_updated > ? ORDER BY time_updated ASC",
			projectPath,
			sinceTimestamp,
		)
		if err != nil {
			return nil, fmt.Errorf("opencode: failed to query updated sessions: %w", err)
		}
	}
	defer rows.Close()

	var sessions []openCodeSessionInfo
	for rows.Next() {
		var s openCodeSessionInfo
		if err := rows.Scan(&s.SessionID, &s.Directory, &s.Title, &s.Slug, &s.Version, &s.TimeCreated, &s.TimeUpdated); err != nil {
			continue
		}
		sessions = append(sessions, s)
	}

	return sessions, nil
}
