package opencode

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi"
	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi/schema"
)

// Type aliases for convenience - use the shared schema types
type (
	SessionData  = schema.SessionData
	ProviderInfo = schema.ProviderInfo
	Exchange     = schema.Exchange
	Message      = schema.Message
	ContentPart  = schema.ContentPart
	ToolInfo     = schema.ToolInfo
	Usage        = schema.Usage
)

// openCodeMessageData represents the JSON structure of a message's data field.
type openCodeMessageData struct {
	Role string `json:"role"`
	Time struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Agent string `json:"agent"`
	Model *struct {
		ProviderID string `json:"providerID"`
		ModelID    string `json:"modelID"`
	} `json:"model,omitempty"`
	ModelID    string `json:"modelID,omitempty"`
	ProviderID string `json:"providerID,omitempty"`
	Path       *struct {
		CWD  string `json:"cwd"`
		Root string `json:"root"`
	} `json:"path,omitempty"`
	Tokens *struct {
		Input     int `json:"input"`
		Output    int `json:"output"`
		Reasoning int `json:"reasoning"`
		Cache     *struct {
			Read  int `json:"read"`
			Write int `json:"write"`
		} `json:"cache,omitempty"`
	} `json:"tokens,omitempty"`
	Cost    float64 `json:"cost,omitempty"`
	Finish  string  `json:"finish,omitempty"`
	Summary *struct {
		Title string `json:"title"`
	} `json:"summary,omitempty"`
}

// openCodePartData represents the JSON structure of a part's data field.
type openCodePartData struct {
	Type   string  `json:"type"`
	Text   string  `json:"text,omitempty"`
	Reason string  `json:"reason,omitempty"`
	Cost   float64 `json:"cost,omitempty"`
	Tokens *struct {
		Input     int `json:"input"`
		Output    int `json:"output"`
		Reasoning int `json:"reasoning"`
		Cache     *struct {
			Read  int `json:"read"`
			Write int `json:"write"`
		} `json:"cache,omitempty"`
	} `json:"tokens,omitempty"`
	Tool   string                 `json:"tool,omitempty"`
	CallID string                 `json:"callID,omitempty"`
	State  map[string]interface{} `json:"state,omitempty"`
}

// GenerateAgentSession creates a SessionData from OpenCode SQLite data.
func GenerateAgentSession(db *sql.DB, sessionInfo *openCodeSessionInfo, workspaceRoot string) (*SessionData, error) {
	slog.Info("GenerateAgentSession: Starting", "sessionID", sessionInfo.SessionID)

	// Use provided workspaceRoot or fall back to session directory
	if workspaceRoot == "" {
		workspaceRoot = sessionInfo.Directory
	}

	// Build exchanges from database
	exchanges, err := buildExchangesFromDB(db, sessionInfo.SessionID, workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to build exchanges: %w", err)
	}

	// Skip empty sessions
	if len(exchanges) == 0 {
		slog.Debug("GenerateAgentSession: Skipping empty session", "sessionID", sessionInfo.SessionID)
		return nil, nil
	}

	// Assign exchangeId to each exchange
	for i := range exchanges {
		exchanges[i].ExchangeID = fmt.Sprintf("%s:%d", sessionInfo.SessionID, i)
	}

	// Populate Summary and FormattedMarkdown for all tools
	for i := range exchanges {
		for j := range exchanges[i].Messages {
			msg := &exchanges[i].Messages[j]
			if msg.Tool != nil {
				summary, formattedMd := formatToolWithSummary(msg.Tool, workspaceRoot)
				if summary != "" {
					msg.Tool.Summary = &summary
				}
				if formattedMd != "" {
					msg.Tool.FormattedMarkdown = &formattedMd
				}
			}
		}
	}

	// Convert timestamp from milliseconds to ISO 8601
	createdAt := convertMsToISO8601(sessionInfo.TimeCreated)

	sessionData := &SessionData{
		SchemaVersion: "1.0",
		Provider: ProviderInfo{
			ID:      "opencode",
			Name:    "OpenCode",
			Version: sessionInfo.Version,
		},
		SessionID:     sessionInfo.SessionID,
		CreatedAt:     createdAt,
		WorkspaceRoot: workspaceRoot,
		Exchanges:     exchanges,
	}

	return sessionData, nil
}

// buildExchangesFromDB queries messages and parts from the database and builds exchanges.
func buildExchangesFromDB(db *sql.DB, sessionID string, workspaceRoot string) ([]Exchange, error) {
	// Query messages for this session
	rows, err := db.Query(
		"SELECT id, data FROM message WHERE session_id = ? ORDER BY time_created ASC",
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var exchanges []Exchange
	var currentExchange *Exchange
	messageIndex := 0

	for rows.Next() {
		var msgID string
		var dataStr string
		if err := rows.Scan(&msgID, &dataStr); err != nil {
			slog.Debug("buildExchangesFromDB: Failed to scan message", "error", err)
			continue
		}

		// Parse message data
		var msgData openCodeMessageData
		if err := json.Unmarshal([]byte(dataStr), &msgData); err != nil {
			slog.Debug("buildExchangesFromDB: Failed to parse message data", "error", err)
			continue
		}

		// Query parts for this message
		parts, err := queryPartsForMessage(db, msgID)
		if err != nil {
			slog.Debug("buildExchangesFromDB: Failed to query parts", "error", err)
			continue
		}

		// Convert timestamp
		timestamp := convertMsToISO8601(msgData.Time.Created)

		// Get model name
		modelName := ""
		if msgData.Model != nil {
			modelName = msgData.Model.ModelID
		} else if msgData.ModelID != "" {
			modelName = msgData.ModelID
		}

		switch msgData.Role {
		case "user":
			// Start a new exchange
			if currentExchange != nil && len(currentExchange.Messages) > 0 {
				exchanges = append(exchanges, *currentExchange)
			}

			currentExchange = &Exchange{
				StartTime: timestamp,
				Messages:  []Message{},
			}

			// Build user message content from parts
			var content []ContentPart
			for _, part := range parts {
				if part.Type == "text" && part.Text != "" {
					content = append(content, ContentPart{Type: "text", Text: part.Text})
				}
			}

			// If no text parts, check if there's a summary
			if len(content) == 0 && msgData.Summary != nil && msgData.Summary.Title != "" {
				content = append(content, ContentPart{Type: "text", Text: msgData.Summary.Title})
			}

			if len(content) > 0 {
				userMsg := Message{
					ID:        fmt.Sprintf("u_%d", messageIndex),
					Timestamp: timestamp,
					Role:      "user",
					Content:   content,
				}
				currentExchange.Messages = append(currentExchange.Messages, userMsg)
			}

		case "assistant":
			if currentExchange == nil {
				currentExchange = &Exchange{
					StartTime: timestamp,
					Messages:  []Message{},
				}
			}

			// Process assistant parts
			for _, part := range parts {
				switch part.Type {
				case "text":
					if part.Text != "" {
						agentMsg := Message{
							ID:        fmt.Sprintf("a_%d", messageIndex),
							Timestamp: timestamp,
							Role:      "agent",
							Model:     modelName,
							Content: []ContentPart{
								{Type: "text", Text: part.Text},
							},
						}
						currentExchange.Messages = append(currentExchange.Messages, agentMsg)
					}

				case "reasoning":
					if part.Text != "" {
						reasoningMsg := Message{
							ID:        fmt.Sprintf("r_%d", messageIndex),
							Timestamp: timestamp,
							Role:      "agent",
							Model:     modelName,
							Content: []ContentPart{
								{Type: "thinking", Text: part.Text},
							},
						}
						currentExchange.Messages = append(currentExchange.Messages, reasoningMsg)
					}

				case "tool":
					toolName := part.Tool
					if toolName == "" {
						continue
					}

					// Extract input from state
					input := make(map[string]interface{})
					if inputVal, ok := part.State["input"]; ok {
						if inputMap, ok := inputVal.(map[string]interface{}); ok {
							input = inputMap
						}
					}

					// Extract output from state
					output := make(map[string]interface{})
					if outputVal, ok := part.State["output"]; ok {
						switch v := outputVal.(type) {
						case string:
							output = map[string]interface{}{"raw": v}
						case map[string]interface{}:
							output = v
						}
					}

					toolMsg := Message{
						ID:        fmt.Sprintf("t_%d", messageIndex),
						Timestamp: timestamp,
						Role:      "agent",
						Model:     modelName,
						Tool: &ToolInfo{
							Name:   toolName,
							Type:   classifyToolType(toolName),
							UseID:  part.CallID,
							Input:  input,
							Output: output,
						},
						PathHints: extractPathHints(toolName, input, workspaceRoot),
					}
					currentExchange.Messages = append(currentExchange.Messages, toolMsg)

				case "step-finish":
					// Extract token usage from step-finish
					if part.Tokens != nil {
						usage := &Usage{
							InputTokens:           part.Tokens.Input,
							OutputTokens:          part.Tokens.Output,
							ReasoningOutputTokens: part.Tokens.Reasoning,
						}
						if part.Tokens.Cache != nil {
							usage.CacheReadInputTokens = part.Tokens.Cache.Read
							usage.CacheCreationInputTokens = part.Tokens.Cache.Write
						}
						// Attach to the most recent agent message
						if currentExchange != nil && len(currentExchange.Messages) > 0 {
							for j := len(currentExchange.Messages) - 1; j >= 0; j-- {
								if currentExchange.Messages[j].Role == "agent" {
									currentExchange.Messages[j].Usage = usage
									break
								}
							}
						}
					}
				}
			}

			currentExchange.EndTime = timestamp
		}

		messageIndex++
	}

	// Add the last exchange if exists
	if currentExchange != nil && len(currentExchange.Messages) > 0 {
		exchanges = append(exchanges, *currentExchange)
	}

	return exchanges, nil
}

// queryPartsForMessage queries all parts for a given message ID.
func queryPartsForMessage(db *sql.DB, messageID string) ([]openCodePartData, error) {
	rows, err := db.Query(
		"SELECT data FROM part WHERE message_id = ? ORDER BY time_created ASC",
		messageID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var parts []openCodePartData
	for rows.Next() {
		var dataStr string
		if err := rows.Scan(&dataStr); err != nil {
			continue
		}
		var part openCodePartData
		if err := json.Unmarshal([]byte(dataStr), &part); err != nil {
			continue
		}
		parts = append(parts, part)
	}

	return parts, nil
}

// classifyToolType maps OpenCode tool names to standard tool types.
func classifyToolType(toolName string) string {
	switch toolName {
	case "read", "glob", "grep":
		return "read"
	case "edit", "write":
		return "write"
	case "bash":
		return "shell"
	case "todowrite":
		return "task"
	case "webfetch", "skill", "task", "question":
		return "generic"
	default:
		return "unknown"
	}
}

// extractPathHints extracts file paths from tool inputs.
func extractPathHints(toolName string, input map[string]interface{}, workspaceRoot string) []string {
	var paths []string

	switch toolName {
	case "read", "edit", "write":
		if filePath, ok := input["filePath"].(string); ok && filePath != "" {
			normalizedPath := spi.NormalizePath(filePath, workspaceRoot)
			if !slices.Contains(paths, normalizedPath) {
				paths = append(paths, normalizedPath)
			}
		}
	case "glob":
		if path, ok := input["path"].(string); ok && path != "" {
			normalizedPath := spi.NormalizePath(path, workspaceRoot)
			if !slices.Contains(paths, normalizedPath) {
				paths = append(paths, normalizedPath)
			}
		}
	case "bash":
		if command, ok := input["command"].(string); ok && command != "" {
			cwd, _ := input["workdir"].(string)
			if cwd == "" {
				cwd = workspaceRoot
			}
			shellPaths := spi.ExtractShellPathHints(command, cwd, workspaceRoot)
			for _, sp := range shellPaths {
				if !slices.Contains(paths, sp) {
					paths = append(paths, sp)
				}
			}
		}
	}

	return paths
}

// formatToolWithSummary generates custom summary and formatted markdown for an OpenCode tool.
func formatToolWithSummary(tool *ToolInfo, workspaceRoot string) (string, string) {
	var summary string
	var formattedMd strings.Builder

	if tool.Input != nil {
		switch tool.Name {
		case "bash":
			command, _ := tool.Input["command"].(string)
			if command != "" {
				summary = fmt.Sprintf("Tool use: **%s** `%s`", tool.Name, truncateString(command, 80))
				formattedMd.WriteString(fmt.Sprintf("```bash\n%s\n```", command))
			}
		case "read", "edit", "write":
			filePath, _ := tool.Input["filePath"].(string)
			if filePath != "" {
				summary = fmt.Sprintf("Tool use: **%s** `%s`", tool.Name, filePath)
			}
		case "glob":
			pattern, _ := tool.Input["pattern"].(string)
			if pattern != "" {
				summary = fmt.Sprintf("Tool use: **%s** `%s`", tool.Name, pattern)
			}
		case "grep":
			pattern, _ := tool.Input["pattern"].(string)
			if pattern != "" {
				summary = fmt.Sprintf("Tool use: **%s** `%s`", tool.Name, pattern)
			}
		default:
			summary = fmt.Sprintf("Tool use: **%s**", tool.Name)
		}
	}

	// Format tool output if present
	if tool.Output != nil {
		if outputStr, ok := tool.Output["raw"].(string); ok {
			cleaned := strings.TrimSpace(outputStr)
			if cleaned != "" {
				if formattedMd.Len() > 0 {
					formattedMd.WriteString("\n")
				}
				if len(cleaned) > 5000 {
					cleaned = cleaned[:5000] + "\n... (truncated)"
				}
				formattedMd.WriteString("```\n" + cleaned + "\n```")
			}
		}
	}

	return summary, strings.TrimSpace(formattedMd.String())
}

// truncateString truncates a string to the specified length.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// convertMsToISO8601 converts a Unix millisecond timestamp to ISO 8601 format.
func convertMsToISO8601(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// extractFirstUserMessageFromDB finds the first user message text for a session.
// Checks Summary.Title first, then falls back to querying text parts.
func extractFirstUserMessageFromDB(db *sql.DB, sessionID string) string {
	rows, err := db.Query(
		"SELECT id, data FROM message WHERE session_id = ? ORDER BY time_created ASC LIMIT 1",
		sessionID,
	)
	if err != nil {
		return ""
	}
	defer rows.Close()

	for rows.Next() {
		var msgID string
		var dataStr string
		if err := rows.Scan(&msgID, &dataStr); err != nil {
			continue
		}

		var msgData openCodeMessageData
		if err := json.Unmarshal([]byte(dataStr), &msgData); err != nil {
			continue
		}

		if msgData.Role == "user" {
			// Check Summary.Title first
			if msgData.Summary != nil && msgData.Summary.Title != "" {
				return msgData.Summary.Title
			}

			// Fall back to querying text parts
			partRows, err := db.Query(
				"SELECT data FROM part WHERE message_id = ? ORDER BY time_created ASC",
				msgID,
			)
			if err != nil {
				continue
			}

			for partRows.Next() {
				var partDataStr string
				if err := partRows.Scan(&partDataStr); err != nil {
					continue
				}
				var part openCodePartData
				if err := json.Unmarshal([]byte(partDataStr), &part); err != nil {
					continue
				}
				if part.Type == "text" && part.Text != "" {
					partRows.Close()
					return part.Text
				}
			}
			partRows.Close()
		}
	}

	return ""
}

// deriveSlug generates a slug from session info and first user message.
func deriveSlug(sessionInfo *openCodeSessionInfo, firstUserMsg string) string {
	// Use session title if available
	if sessionInfo.Title != "" {
		slug := spi.GenerateFilenameFromUserMessage(sessionInfo.Title)
		if slug != "" {
			return slug
		}
	}
	// Fall back to first user message
	if firstUserMsg != "" {
		return spi.GenerateFilenameFromUserMessage(firstUserMsg)
	}
	return ""
}
