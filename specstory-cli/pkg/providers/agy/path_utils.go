package agy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi"
)

// HistoryLine represents a line in ~/.gemini/antigravity-cli/history.jsonl
type HistoryLine struct {
	Display        string `json:"display"`
	Timestamp      int64  `json:"timestamp"`
	Workspace      string `json:"workspace"`
	ConversationID string `json:"conversationId,omitempty"`
}

func getAgyDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}
	return filepath.Join(home, ".gemini", "antigravity-cli"), nil
}

func getHistoryPath() (string, error) {
	agyDir, err := getAgyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(agyDir, "history.jsonl"), nil
}

func getBrainDir() (string, error) {
	agyDir, err := getAgyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(agyDir, "brain"), nil
}

func readHistoryLines() ([]HistoryLine, error) {
	historyPath, err := getHistoryPath()
	if err != nil {
		return nil, err
	}
	file, err := os.Open(historyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var lines []HistoryLine
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		text := scanner.Text()
		if strings.TrimSpace(text) == "" {
			continue
		}
		var line HistoryLine
		if err := json.Unmarshal([]byte(text), &line); err == nil {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}

func normalizePath(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	canonical, err := spi.GetCanonicalPath(abs)
	if err != nil {
		canonical = abs
	}
	return canonical
}
