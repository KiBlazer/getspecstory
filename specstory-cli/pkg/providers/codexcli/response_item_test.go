package codexcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi"
)

func TestResponseItemSessionWatcher(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	data := `{"type":"session_meta","timestamp":"2026-11-03T02:00:00Z","payload":{"id":"test-session","timestamp":"2026-11-03T02:00:00Z","cwd":"` + dir + `"}}
{"type":"response_item","timestamp":"2026-11-03T02:00:01Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>test</environment_context>"}]}}
{"type":"response_item","timestamp":"2026-11-03T02:00:02Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}
{"type":"response_item","timestamp":"2026-11-03T02:00:03Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi there"}]}}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	ch := make(chan *spi.AgentChatSession, 1)
	if err := processCodexSessionFile(path, dir, normalizeCodexPath(dir), func(s *spi.AgentChatSession) { ch <- s }); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-ch:
		if s.Slug == "" {
			t.Fatal("missing slug")
		}
		session := s.SessionData
		if session == nil {
			t.Fatal("missing session data")
		}
		if len(session.Exchanges) != 1 || len(session.Exchanges[0].Messages) != 2 {
			t.Fatalf("unexpected exchanges: %+v", session.Exchanges)
		}
		if session.Exchanges[0].Messages[0].Content[0].Text != "hello" || session.Exchanges[0].Messages[1].Content[0].Text != "Hi there" {
			t.Fatalf("unexpected messages: %+v", session.Exchanges[0].Messages)
		}
	case <-time.After(time.Second):
		t.Fatal("watcher skipped session; no save callback")
	}
	sessionMeta, err := loadCodexSessionMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := extractCodexSessionMetadata(&codexSessionInfo{SessionPath: path, Meta: sessionMeta})
	if err != nil || meta == nil {
		t.Fatalf("session not listed: metadata=%v error=%v", meta, err)
	}
}

func TestResponseItemsDoNotDuplicateLegacyEvents(t *testing.T) {
	const data = `[{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}},{"type":"event_msg","payload":{"type":"user_message","message":"hello"}},{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}},{"type":"event_msg","payload":{"type":"agent_message","message":"hi"}}]`
	var records []map[string]interface{}
	if err := json.Unmarshal([]byte(data), &records); err != nil {
		t.Fatal(err)
	}
	exchanges, err := buildExchangesFromRecords(records, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(exchanges) != 1 || len(exchanges[0].Messages) != 2 {
		t.Fatalf("duplicate messages: %+v", exchanges)
	}
}
