package agy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

	// Populate FormattedMarkdown for all tools using customized markdown tools formatter
	for i := range exchanges {
		for j := range exchanges[i].Messages {
			msg := &exchanges[i].Messages[j]
			if msg.Tool != nil {
				formattedMd := formatToolAsMarkdown(msg.Tool)
				msg.Tool.FormattedMarkdown = &formattedMd
			}
		}
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

func formatToolAsMarkdown(tool *schema.ToolInfo) string {
	if tool == nil {
		return ""
	}

	var builder strings.Builder

	// 1. Build custom summary for certain tools (appending key parameters)
	var customSummary string
	switch tool.Name {
	case "view_file":
		if filePath, ok := tool.Input["AbsolutePath"].(string); ok && filePath != "" {
			filePath = strings.Trim(filePath, `"'`)
			customSummary = fmt.Sprintf("Tool use: **%s** `%s`", tool.Name, filepath.Base(filePath))
		}
	case "write_to_file":
		if filePath, ok := tool.Input["TargetFile"].(string); ok && filePath != "" {
			filePath = strings.Trim(filePath, `"'`)
			customSummary = fmt.Sprintf("Tool use: **%s** `%s`", tool.Name, filepath.Base(filePath))
		}
	case "replace_file_content", "multi_replace_file_content":
		if filePath, ok := tool.Input["TargetFile"].(string); ok && filePath != "" {
			filePath = strings.Trim(filePath, `"'`)
			customSummary = fmt.Sprintf("Tool use: **%s** `%s`", tool.Name, filepath.Base(filePath))
		}
	case "list_dir":
		if dirPath, ok := tool.Input["DirectoryPath"].(string); ok && dirPath != "" {
			dirPath = strings.Trim(dirPath, `"'`)
			customSummary = fmt.Sprintf("Tool use: **%s** `%s`", tool.Name, dirPath)
		}
	case "run_command":
		if cmd, ok := tool.Input["CommandLine"].(string); ok && cmd != "" {
			cmd = strings.Trim(cmd, `"'`)
			customSummary = fmt.Sprintf("Tool use: **%s** `%s`", tool.Name, cmd)
		}
	case "grep_search":
		query := fmt.Sprintf("%v", tool.Input["Query"])
		query = strings.Trim(query, `"'`)
		customSummary = fmt.Sprintf("Tool use: **%s** `%s`", tool.Name, query)
	case "search_web":
		query := fmt.Sprintf("%v", tool.Input["query"])
		query = strings.Trim(query, `"'`)
		customSummary = fmt.Sprintf("Tool use: **%s** `%s`", tool.Name, query)
	}

	if customSummary != "" {
		tool.Summary = &customSummary
	}

	// 2. Format Input Section
	if len(tool.Input) > 0 {
		// Sort keys for deterministic output order
		keys := make([]string, 0, len(tool.Input))
		for k := range tool.Input {
			if !strings.HasPrefix(k, "_") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)

		// Filter out inputs that are already clear from the summary to keep it clean
		hasImportantInputs := false
		for _, k := range keys {
			// Skip parameters already highlighted in the summary
			if tool.Name == "view_file" && k == "AbsolutePath" {
				continue
			}
			if tool.Name == "write_to_file" && k == "TargetFile" {
				continue
			}
			if (tool.Name == "replace_file_content" || tool.Name == "multi_replace_file_content") && k == "TargetFile" {
				continue
			}
			if tool.Name == "list_dir" && k == "DirectoryPath" {
				continue
			}
			if tool.Name == "run_command" && k == "CommandLine" {
				continue
			}
			if tool.Name == "grep_search" && k == "Query" {
				continue
			}
			if tool.Name == "search_web" && k == "query" {
				continue
			}
			hasImportantInputs = true
		}

		if hasImportantInputs {
			builder.WriteString("\n**Input:**\n\n")
			for _, k := range keys {
				if tool.Name == "view_file" && k == "AbsolutePath" {
					continue
				}
				if tool.Name == "write_to_file" && k == "TargetFile" {
					continue
				}
				if (tool.Name == "replace_file_content" || tool.Name == "multi_replace_file_content") && k == "TargetFile" {
					continue
				}
				if tool.Name == "list_dir" && k == "DirectoryPath" {
					continue
				}
				if tool.Name == "run_command" && k == "CommandLine" {
					continue
				}
				if tool.Name == "grep_search" && k == "Query" {
					continue
				}
				if tool.Name == "search_web" && k == "query" {
					continue
				}

				val := tool.Input[k]
				if valStr, ok := val.(string); ok && strings.Contains(valStr, "\n") {
					builder.WriteString(fmt.Sprintf("- %s:\n```text\n%s\n```\n", k, valStr))
				} else {
					builder.WriteString(fmt.Sprintf("- %s: `%v`\n", k, val))
				}
			}
			builder.WriteString("\n")
		}
	}

	// 3. Format Output/Result Section
	if tool.Output != nil {
		if content, ok := tool.Output["output"].(string); ok && content != "" {
			builder.WriteString("\n**Result:**\n\n")
			lang := "text"
			if tool.Name == "view_file" || tool.Name == "write_to_file" {
				var filePath string
				if fp, ok := tool.Input["AbsolutePath"].(string); ok {
					filePath = fp
				} else if fp, ok := tool.Input["TargetFile"].(string); ok {
					filePath = fp
				}
				if filePath != "" {
					ext := strings.TrimPrefix(filepath.Ext(filePath), ".")
					if ext != "" {
						lang = strings.ToLower(ext)
					}
				}
			} else if tool.Name == "run_command" {
				lang = "bash"
			} else if tool.Name == "list_dir" || tool.Name == "grep_search" {
				if strings.HasPrefix(content, "{") || strings.HasPrefix(content, "[") {
					lang = "json"
				}
			}

			if strings.Contains(content, "\n") {
				builder.WriteString(fmt.Sprintf("```%s\n%s\n```", lang, content))
			} else {
				builder.WriteString(fmt.Sprintf("`%s`", content))
			}
		}
	}

	return builder.String()
}
