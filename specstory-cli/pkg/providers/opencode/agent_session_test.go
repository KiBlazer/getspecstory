package opencode

import (
	"strings"
	"testing"
)

func TestClassifyToolType(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		expected string
	}{
		{"read tool", "read", "read"},
		{"glob tool", "glob", "read"},
		{"grep tool", "grep", "read"},
		{"edit tool", "edit", "write"},
		{"write tool", "write", "write"},
		{"bash tool", "bash", "shell"},
		{"todowrite tool", "todowrite", "task"},
		{"webfetch tool", "webfetch", "generic"},
		{"skill tool", "skill", "generic"},
		{"task tool", "task", "generic"},
		{"question tool", "question", "generic"},
		{"unknown tool", "foobar", "unknown"},
		{"empty tool", "", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyToolType(tt.toolName)
			if result != tt.expected {
				t.Errorf("classifyToolType(%q) = %q, want %q", tt.toolName, result, tt.expected)
			}
		})
	}
}

func TestConvertMsToISO8601(t *testing.T) {
	tests := []struct {
		name     string
		ms       int64
		expected string
	}{
		{"zero", 0, ""},
		{"negative", -1, ""},
		{"valid timestamp", 1778656458683, "2026-05-13T07:14:18Z"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertMsToISO8601(tt.ms)
			if result != tt.expected {
				t.Errorf("convertMsToISO8601(%d) = %q, want %q", tt.ms, result, tt.expected)
			}
		})
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		maxLen   int
		expected string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncated", "hello world", 8, "hello..."},
		{"empty", "", 5, ""},
		{"single char", "a", 1, "a"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateString(tt.s, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.s, tt.maxLen, result, tt.expected)
			}
		})
	}
}

func TestExpandTilde(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		contains string // Check if result contains this string
	}{
		{"empty", "", ""},
		{"no tilde", "/usr/local/bin", "/usr/local/bin"},
		{"tilde only", "~", "/home"},
		{"tilde slash", "~/Documents", "/Documents"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandTilde(tt.path)
			if tt.contains != "" && !strings.Contains(result, tt.contains) {
				t.Errorf("expandTilde(%q) = %q, want it to contain %q", tt.path, result, tt.contains)
			}
		})
	}
}

func TestNormalizeOpenCodePath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		empty    bool
		contains string
	}{
		{"empty", "", true, ""},
		{"spaces", "   ", true, ""},
		{"relative", ".", false, ""},
		{"absolute", "/tmp", false, "/tmp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeOpenCodePath(tt.path)
			if tt.empty && result != "" {
				t.Errorf("normalizeOpenCodePath(%q) = %q, want empty", tt.path, result)
			}
			if !tt.empty && result == "" {
				t.Errorf("normalizeOpenCodePath(%q) = %q, want non-empty", tt.path, result)
			}
		})
	}
}

func TestSessionMatchesProject(t *testing.T) {
	tests := []struct {
		name        string
		sessionDir  string
		projectPath string
		expected    bool
	}{
		{"exact match", "/home/user/project", "/home/user/project", true},
		{"different paths", "/home/user/project1", "/home/user/project2", false},
		{"trailing slash", "/home/user/project/", "/home/user/project", true},
		{"dot path", "/home/user/project/.", "/home/user/project", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sessionMatchesProject(tt.sessionDir, tt.projectPath)
			if result != tt.expected {
				t.Errorf("sessionMatchesProject(%q, %q) = %v, want %v",
					tt.sessionDir, tt.projectPath, result, tt.expected)
			}
		})
	}
}

func TestDeriveSlug(t *testing.T) {
	tests := []struct {
		name         string
		sessionInfo  *openCodeSessionInfo
		firstUserMsg string
		expected     string
	}{
		{
			"with title",
			&openCodeSessionInfo{Title: "Hello World Test"},
			"",
			"hello-world-test",
		},
		{
			"with user message",
			&openCodeSessionInfo{Title: ""},
			"My first message",
			"my-first-message",
		},
		{
			"title preferred over message",
			&openCodeSessionInfo{Title: "Title"},
			"Message",
			"title",
		},
		{
			"empty both",
			&openCodeSessionInfo{Title: ""},
			"",
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := deriveSlug(tt.sessionInfo, tt.firstUserMsg)
			if result != tt.expected {
				t.Errorf("deriveSlug() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestExtractPathHints(t *testing.T) {
	tests := []struct {
		name        string
		toolName    string
		input       map[string]interface{}
		expectCount int
	}{
		{
			"read with filePath",
			"read",
			map[string]interface{}{"filePath": "/home/user/file.go"},
			1,
		},
		{
			"edit with filePath",
			"edit",
			map[string]interface{}{"filePath": "/home/user/file.go"},
			1,
		},
		{
			"glob with path",
			"glob",
			map[string]interface{}{"path": "/home/user"},
			1,
		},
		{
			"unknown tool",
			"unknown",
			map[string]interface{}{},
			0,
		},
		{
			"empty input",
			"read",
			map[string]interface{}{},
			0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractPathHints(tt.toolName, tt.input, "/workspace")
			if len(result) != tt.expectCount {
				t.Errorf("extractPathHints() returned %d paths, want %d", len(result), tt.expectCount)
			}
		})
	}
}
