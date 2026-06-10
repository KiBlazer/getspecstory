package agy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"

	"time"

	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi/schema"
)

// Step represents a single line in transcript.jsonl
type Step struct {
	StepIndex int            `json:"step_index"`
	Source    string         `json:"source"`
	Type      string         `json:"type"`
	Status    string         `json:"status"`
	CreatedAt string         `json:"created_at"`
	Content   string         `json:"content"`
	Thinking  string         `json:"thinking"`
	ToolCalls []StepToolCall `json:"tool_calls"`
}

type StepToolCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

func cleanArgValue(v interface{}) interface{} {
	if s, ok := v.(string); ok {
		// Clean nested quotes if they exist (e.g. "\"/path\"" -> "/path")
		if strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"") && len(s) >= 2 {
			unquoted := s[1 : len(s)-1]
			return cleanArgValue(unquoted)
		}
		return s
	}
	if m, ok := v.(map[string]interface{}); ok {
		res := make(map[string]interface{})
		for k, val := range m {
			res[k] = cleanArgValue(val)
		}
		return res
	}
	return v
}

func classifyToolType(name string) string {
	name = strings.ToLower(name)
	if strings.Contains(name, "write") || strings.Contains(name, "replace") {
		return "write"
	}
	if strings.Contains(name, "read") || strings.Contains(name, "view") {
		return "read"
	}
	if strings.Contains(name, "search") || strings.Contains(name, "grep") || strings.Contains(name, "find") {
		return "search"
	}
	if strings.Contains(name, "run_command") || strings.Contains(name, "bash") || strings.Contains(name, "execute") {
		return "shell"
	}
	if strings.Contains(name, "task") || strings.Contains(name, "todo") || strings.Contains(name, "schedule") {
		return "task"
	}
	return "generic"
}

// ParseTranscript parses transcript.jsonl file into schema.SessionData
func ParseTranscript(conversationID string, workspaceRoot string) (*schema.SessionData, error) {
	brainDir, err := getBrainDir()
	if err != nil {
		return nil, err
	}

	transcriptPath := filepath.Join(brainDir, conversationID, ".system_generated", "logs", "transcript.jsonl")
	file, err := os.Open(transcriptPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open transcript file: %w", err)
	}
	defer file.Close()

	var exchanges []schema.Exchange
	var currentExchange *schema.Exchange
	var pendingToolMessages []*schema.Message

	scanner := bufio.NewScanner(file)
	// Larger buffer for massive lines (outputs of commands/files)
	const maxCapacity = 16 * 1024 * 1024 // 16MB
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, maxCapacity)

	firstCreatedAt := ""

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var step Step
		if err := json.Unmarshal([]byte(line), &step); err != nil {
			continue
		}

		if firstCreatedAt == "" && step.CreatedAt != "" {
			firstCreatedAt = step.CreatedAt
		}

		switch step.Type {
		case "USER_INPUT":
			// Complete previous exchange if it exists
			if currentExchange != nil && len(currentExchange.Messages) > 0 {
				exchanges = append(exchanges, *currentExchange)
			}

			currentExchange = &schema.Exchange{
				ExchangeID: fmt.Sprintf("%s:%d", conversationID, len(exchanges)),
				StartTime:  step.CreatedAt,
				Messages:   []schema.Message{},
			}

			userContent := strings.TrimSpace(step.Content)
			// Strip common enclosing tags if present
			userContent = strings.TrimPrefix(userContent, "<USER_REQUEST>\n")
			if idx := strings.Index(userContent, "</USER_REQUEST>"); idx != -1 {
				userContent = userContent[:idx]
			}
			userContent = strings.TrimSpace(userContent)

			currentExchange.Messages = append(currentExchange.Messages, schema.Message{
				Role:      "user",
				Timestamp: step.CreatedAt,
				Content: []schema.ContentPart{
					{Type: "text", Text: userContent},
				},
			})

		case "PLANNER_RESPONSE":
			if currentExchange == nil {
				currentExchange = &schema.Exchange{
					ExchangeID: fmt.Sprintf("%s:%d", conversationID, len(exchanges)),
					StartTime:  step.CreatedAt,
					Messages:   []schema.Message{},
				}
			}

			// Add thinking if present
			if step.Thinking != "" {
				currentExchange.Messages = append(currentExchange.Messages, schema.Message{
					Role:      "agent",
					Timestamp: step.CreatedAt,
					Content: []schema.ContentPart{
						{Type: "thinking", Text: step.Thinking},
					},
				})
			}

			// Add tool calls
			if len(step.ToolCalls) > 0 {
				for i, tc := range step.ToolCalls {
					cleanedArgs := make(map[string]interface{})
					for k, v := range tc.Args {
						cleanedArgs[k] = cleanArgValue(v)
					}

					toolMsg := schema.Message{
						Role:      "agent",
						Timestamp: step.CreatedAt,
						Tool: &schema.ToolInfo{
							Name:  tc.Name,
							Type:  classifyToolType(tc.Name),
							UseID: fmt.Sprintf("%s_%d_%d", conversationID, step.StepIndex, i),
							Input: cleanedArgs,
						},
					}
					currentExchange.Messages = append(currentExchange.Messages, toolMsg)
					// Track this message as pending tool result mapping
					// We point to the message inside the Exchange's slice to update it in-place later
					pendingToolMessages = append(pendingToolMessages, &currentExchange.Messages[len(currentExchange.Messages)-1])
				}
			}

			// Add final content text response
			if step.Content != "" {
				currentExchange.Messages = append(currentExchange.Messages, schema.Message{
					Role:      "agent",
					Timestamp: step.CreatedAt,
					Content: []schema.ContentPart{
						{Type: "text", Text: step.Content},
					},
				})
			}

		default:
			// Tool result step (e.g. VIEW_FILE, RUN_COMMAND, etc.)
			if len(pendingToolMessages) > 0 {
				targetMsg := pendingToolMessages[0]
				pendingToolMessages = pendingToolMessages[1:]

				if targetMsg.Tool != nil {
					targetMsg.Tool.Output = map[string]interface{}{
						"output": step.Content,
					}
					// Generate FormattedMarkdown
					formatted := fmt.Sprintf("\n\n<pre><code>%s</code></pre>\n", html.EscapeString(step.Content))
					targetMsg.Tool.FormattedMarkdown = &formatted
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan transcript file: %w", err)
	}

	// Append the last exchange
	if currentExchange != nil && len(currentExchange.Messages) > 0 {
		exchanges = append(exchanges, *currentExchange)
	}

	if firstCreatedAt == "" {
		firstCreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	sessionData := &schema.SessionData{
		SchemaVersion: "1.0",
		Provider: schema.ProviderInfo{
			ID:      "agy",
			Name:    "Antigravity",
			Version: "1.0.7",
		},
		SessionID:     conversationID,
		CreatedAt:     firstCreatedAt,
		WorkspaceRoot: workspaceRoot,
		Exchanges:     exchanges,
	}

	return sessionData, nil
}
