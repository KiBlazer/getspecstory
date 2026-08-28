package pi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi"
	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi/schema"
)

type piEntry struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	ParentID   *string         `json:"parentId"`
	Timestamp  string          `json:"timestamp"`
	Message    json.RawMessage `json:"message"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name"`
	Summary    string          `json:"summary"`
	CustomType string          `json:"customType"`
	Display    bool            `json:"display"`
}

type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Timestamp  int64           `json:"timestamp"`
	Provider   string          `json:"provider"`
	Model      string          `json:"model"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	IsError    bool            `json:"isError"`
	Command    string          `json:"command"`
	Output     string          `json:"output"`
}

type piContentBlock struct {
	Type      string                 `json:"type"`
	Text      string                 `json:"text"`
	Thinking  string                 `json:"thinking"`
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

func parsePiSession(path, workspaceRoot string) (*spi.AgentChatSession, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entries, header, err := parsePiEntries(raw)
	if err != nil {
		return nil, err
	}
	active := activePiEntries(entries)
	data := buildPiSession(header, active, workspaceRoot)
	if info, err := os.Stat(path); err == nil {
		data.UpdatedAt = info.ModTime().UTC().Format("2006-01-02T15:04:05Z")
	}
	return &spi.AgentChatSession{SessionID: header.ID, CreatedAt: header.Timestamp, Slug: data.Slug, SessionData: data, RawData: string(raw)}, nil
}

func parsePiEntries(raw []byte) ([]piEntry, sessionHeader, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var entries []piEntry
	var header sessionHeader
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry piEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // A watcher can observe a partially appended final line.
		}
		if header.ID == "" {
			if entry.Type != "session" {
				return nil, sessionHeader{}, fmt.Errorf("Pi session is missing its header")
			}
			if err := json.Unmarshal([]byte(line), &header); err != nil {
				return nil, sessionHeader{}, err
			}
			if header.ID == "" || header.CWD == "" {
				return nil, sessionHeader{}, fmt.Errorf("Pi session header is incomplete")
			}
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, sessionHeader{}, err
	}
	if header.ID == "" {
		return nil, sessionHeader{}, fmt.Errorf("empty Pi session")
	}
	return entries, header, nil
}

func activePiEntries(entries []piEntry) []piEntry {
	byID := make(map[string]piEntry)
	leaf := ""
	for _, entry := range entries {
		if entry.ID == "" {
			return entries // Legacy linear sessions do not carry tree IDs.
		}
		byID[entry.ID] = entry
		leaf = entry.ID
	}
	if leaf == "" {
		return entries
	}
	active := make(map[string]bool)
	for leaf != "" {
		entry, ok := byID[leaf]
		if !ok || active[leaf] {
			break
		}
		active[leaf] = true
		if entry.ParentID == nil {
			break
		}
		leaf = *entry.ParentID
	}
	result := make([]piEntry, 0, len(active))
	for _, entry := range entries {
		if active[entry.ID] {
			result = append(result, entry)
		}
	}
	return result
}

func piContent(raw json.RawMessage) []schema.ContentPart {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if strings.TrimSpace(text) != "" {
			return []schema.ContentPart{{Type: schema.ContentTypeText, Text: text}}
		}
		return nil
	}
	var blocks []piContentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var parts []schema.ContentPart
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				parts = append(parts, schema.ContentPart{Type: schema.ContentTypeText, Text: block.Text})
			}
		case "thinking":
			if block.Thinking != "" {
				parts = append(parts, schema.ContentPart{Type: schema.ContentTypeThinking, Text: block.Thinking})
			}
		}
	}
	return parts
}

func piToolCalls(raw json.RawMessage) []piContentBlock {
	var blocks []piContentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var calls []piContentBlock
	for _, block := range blocks {
		if block.Type == "toolCall" {
			calls = append(calls, block)
		}
	}
	return calls
}

func piToolType(name string) string {
	name = strings.ToLower(name)
	switch {
	case strings.Contains(name, "write") || strings.Contains(name, "edit"):
		return schema.ToolTypeWrite
	case strings.Contains(name, "read"):
		return schema.ToolTypeRead
	case strings.Contains(name, "grep") || strings.Contains(name, "find") || strings.Contains(name, "search") || strings.Contains(name, "ls"):
		return schema.ToolTypeSearch
	case strings.Contains(name, "bash") || strings.Contains(name, "shell"):
		return schema.ToolTypeShell
	default:
		return schema.ToolTypeGeneric
	}
}

func buildPiSession(header sessionHeader, entries []piEntry, workspaceRoot string) *schema.SessionData {
	if workspaceRoot == "" {
		workspaceRoot = header.CWD
	}
	var exchanges []schema.Exchange
	var current *schema.Exchange
	pending := make(map[string]*schema.ToolInfo)
	name, firstUser := "", ""
	ensureExchange := func(timestamp string) *schema.Exchange {
		if current == nil {
			current = &schema.Exchange{ExchangeID: fmt.Sprintf("%s:%d", header.ID, len(exchanges)), StartTime: timestamp}
		}
		return current
	}
	finishExchange := func() {
		if current != nil && len(current.Messages) > 0 {
			exchanges = append(exchanges, *current)
		}
		current = nil
	}
	for _, entry := range entries {
		switch entry.Type {
		case "session_info":
			if strings.TrimSpace(entry.Name) != "" {
				name = strings.TrimSpace(entry.Name)
			}
		case "custom_message":
			// Pi injects this content into the model as a user message. `display`
			// only affects Pi's TUI, so preserve it for exported history either way.
			parts := piContent(entry.Content)
			if len(parts) == 0 {
				continue
			}
			finishExchange()
			current = &schema.Exchange{ExchangeID: fmt.Sprintf("%s:%d", header.ID, len(exchanges)), StartTime: entry.Timestamp}
			current.Messages = append(current.Messages, schema.Message{ID: entry.ID, Role: schema.RoleUser, Timestamp: entry.Timestamp, Content: parts})
		case "message":
			var message piMessage
			if json.Unmarshal(entry.Message, &message) != nil {
				continue
			}
			switch message.Role {
			case "user":
				finishExchange()
				parts := piContent(message.Content)
				if len(parts) == 0 {
					continue
				}
				current = &schema.Exchange{ExchangeID: fmt.Sprintf("%s:%d", header.ID, len(exchanges)), StartTime: entry.Timestamp}
				current.Messages = append(current.Messages, schema.Message{ID: entry.ID, Role: schema.RoleUser, Timestamp: entry.Timestamp, Content: parts})
				if firstUser == "" {
					firstUser = parts[0].Text
				}
			case "assistant":
				exchange := ensureExchange(entry.Timestamp)
				if parts := piContent(message.Content); len(parts) > 0 {
					exchange.Messages = append(exchange.Messages, schema.Message{ID: entry.ID, Role: schema.RoleAgent, Timestamp: entry.Timestamp, Model: message.Model, Content: parts})
				}
				for _, call := range piToolCalls(message.Content) {
					tool := &schema.ToolInfo{Name: call.Name, Type: piToolType(call.Name), UseID: call.ID, Input: call.Arguments}
					exchange.Messages = append(exchange.Messages, schema.Message{ID: entry.ID + ":" + call.ID, Role: schema.RoleAgent, Timestamp: entry.Timestamp, Model: message.Model, Tool: tool})
					// ToolInfo is heap allocated and therefore remains stable even if
					// subsequent appends reallocate exchange.Messages.
					pending[call.ID] = tool
				}
				exchange.EndTime = entry.Timestamp
			case "toolResult":
				if target := pending[message.ToolCallID]; target != nil {
					target.Output = map[string]interface{}{"content": piContentText(message.Content), "isError": message.IsError}
				}
			case "bashExecution":
				exchange := ensureExchange(entry.Timestamp)
				exchange.Messages = append(exchange.Messages, schema.Message{ID: entry.ID, Role: schema.RoleAgent, Timestamp: entry.Timestamp, Tool: &schema.ToolInfo{Name: "bash", Type: schema.ToolTypeShell, UseID: entry.ID, Input: map[string]interface{}{"command": message.Command}, Output: map[string]interface{}{"output": message.Output}}})
			}
		case "compaction", "branch_summary":
			if entry.Summary != "" {
				exchange := ensureExchange(entry.Timestamp)
				exchange.Messages = append(exchange.Messages, schema.Message{ID: entry.ID, Role: schema.RoleAgent, Timestamp: entry.Timestamp, Content: []schema.ContentPart{{Type: schema.ContentTypeThinking, Text: entry.Summary}}})
			}
		}
	}
	finishExchange()
	if name == "" {
		name = firstUser
	}
	return &schema.SessionData{SchemaVersion: "1.0", Provider: schema.ProviderInfo{ID: "pi", Name: "Pi", Version: "unknown"}, SessionID: header.ID, CreatedAt: header.Timestamp, Slug: name, WorkspaceRoot: workspaceRoot, Exchanges: exchanges}
}

func piContentText(raw json.RawMessage) string {
	parts := piContent(raw)
	var text []string
	for _, part := range parts {
		text = append(text, part.Text)
	}
	return strings.Join(text, "\n")
}
