package pi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi"
)

var piUserHomeDir = os.UserHomeDir

type sessionHeader struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
}

type sessionFileInfo struct {
	ID        string
	Path      string
	CreatedAt string
	CWD       string
}

func piSessionsRoot() (string, error) {
	home, err := piUserHomeDir()
	if err != nil {
		return "", fmt.Errorf("pi: cannot resolve home directory: %w", err)
	}
	return filepath.Join(home, ".pi", "agent", "sessions"), nil
}

func normalizePiPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		abs = filepath.Clean(path)
	}
	if canonical, err := spi.GetCanonicalPath(abs); err == nil {
		return canonical
	}
	return abs
}

func readPiSessionHeader(path string) (sessionHeader, error) {
	file, err := os.Open(path)
	if err != nil {
		return sessionHeader{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return sessionHeader{}, err
		}
		return sessionHeader{}, fmt.Errorf("empty session file")
	}
	var header sessionHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return sessionHeader{}, err
	}
	if header.Type != "session" || header.ID == "" || header.CWD == "" {
		return sessionHeader{}, fmt.Errorf("not a Pi session")
	}
	return header, nil
}

func findPiSessions(projectPath, sessionID string) ([]sessionFileInfo, error) {
	root, err := piSessionsRoot()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return []sessionFileInfo{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("pi: cannot access sessions directory: %w", err)
	}

	normalizedProject := normalizePiPath(projectPath)
	var sessions []sessionFileInfo
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		header, err := readPiSessionHeader(path)
		if err != nil || normalizePiPath(header.CWD) != normalizedProject {
			return nil
		}
		if sessionID != "" && header.ID != sessionID {
			return nil
		}
		sessions = append(sessions, sessionFileInfo{ID: header.ID, Path: path, CreatedAt: header.Timestamp, CWD: header.CWD})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pi: scan sessions: %w", err)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt > sessions[j].CreatedAt })
	return sessions, nil
}
