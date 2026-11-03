package codexcli

import "strings"

// codexResponseMessage extracts chat text from app-server rollout records.
func codexResponseMessage(record map[string]interface{}) (string, string) {
	if record["type"] != "response_item" {
		return "", ""
	}
	payload, ok := record["payload"].(map[string]interface{})
	if !ok || payload["type"] != "message" {
		return "", ""
	}
	role, _ := payload["role"].(string)
	if role != "user" && role != "assistant" {
		return "", ""
	}
	parts, _ := payload["content"].([]interface{})
	var texts []string
	for _, part := range parts {
		p, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		kind, _ := p["type"].(string)
		if kind != "input_text" && kind != "output_text" {
			continue
		}
		value, _ := p["text"].(string)
		if value != "" {
			texts = append(texts, value)
		}
	}
	text := strings.Join(texts, "\n")
	if role == "user" && (strings.HasPrefix(strings.TrimSpace(text), "<environment_context>") || strings.HasPrefix(strings.TrimSpace(text), "<user_instructions>")) {
		return "", ""
	}
	return role, text
}

func codexUserMessage(record map[string]interface{}) string {
	if record["type"] == "event_msg" {
		p, _ := record["payload"].(map[string]interface{})
		if p["type"] == "user_message" {
			text, _ := p["message"].(string)
			return text
		}
	}
	role, text := codexResponseMessage(record)
	if role == "user" {
		return text
	}
	return ""
}
