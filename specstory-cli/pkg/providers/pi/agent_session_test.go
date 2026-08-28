package pi

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/specstoryai/getspecstory/specstory-cli/pkg/spi"
)

func TestParsePiSessionUsesActiveBranchAndPairsTools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	contents := `{"type":"session","version":3,"id":"session-1","timestamp":"2026-08-28T00:00:00Z","cwd":"/work/project"}
{"type":"message","id":"user-1","parentId":null,"timestamp":"2026-08-28T00:00:01Z","message":{"role":"user","content":"inspect the project"}}
{"type":"message","id":"assistant-1","parentId":"user-1","timestamp":"2026-08-28T00:00:02Z","message":{"role":"assistant","model":"gpt-test","content":[{"type":"thinking","thinking":"I will inspect it."},{"type":"toolCall","id":"call-1","name":"read","arguments":{"path":"README.md"}},{"type":"toolCall","id":"call-2","name":"bash","arguments":{"command":"pwd"}}]}}
{"type":"message","id":"tool-1","parentId":"assistant-1","timestamp":"2026-08-28T00:00:03Z","message":{"role":"toolResult","toolCallId":"call-1","toolName":"read","content":[{"type":"text","text":"contents"}],"isError":false}}
{"type":"message","id":"tool-2","parentId":"tool-1","timestamp":"2026-08-28T00:00:04Z","message":{"role":"toolResult","toolCallId":"call-2","toolName":"bash","content":[{"type":"text","text":"/work/project"}],"isError":false}}
{"type":"message","id":"old-branch","parentId":"assistant-1","timestamp":"2026-08-28T00:00:04Z","message":{"role":"assistant","content":"discarded branch"}}
{"type":"message","id":"user-2","parentId":"tool-2","timestamp":"2026-08-28T00:00:05Z","message":{"role":"user","content":[{"type":"text","text":"continue"}]}}
{"type":"message","id":"assistant-2","parentId":"user-2","timestamp":"2026-08-28T00:00:06Z","message":{"role":"assistant","model":"gpt-test","content":[{"type":"text","text":"done"}]}}
{"type":"custom_message","id":"custom-1","parentId":"assistant-2","timestamp":"2026-08-28T00:00:07Z","customType":"extension","content":"remember this","display":false}
{"type":"session_info","id":"name-1","parentId":"custom-1","timestamp":"2026-08-28T00:00:08Z","name":"Pi integration"}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := parsePiSession(path, "/work/project")
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionID != "session-1" || session.Slug != "Pi integration" {
		t.Fatalf("unexpected session identity: %#v", session)
	}
	if len(session.SessionData.Exchanges) != 3 {
		t.Fatalf("exchanges = %d, want 3", len(session.SessionData.Exchanges))
	}
	first := session.SessionData.Exchanges[0]
	if len(first.Messages) != 4 {
		t.Fatalf("first exchange messages = %d, want 4", len(first.Messages))
	}
	tool := first.Messages[2].Tool
	if tool == nil || tool.Name != "read" || tool.Output["content"] != "contents" {
		t.Fatalf("tool not paired correctly: %#v", tool)
	}
	secondTool := first.Messages[3].Tool
	if secondTool == nil || secondTool.Name != "bash" || secondTool.Output["content"] != "/work/project" {
		t.Fatalf("second tool not paired correctly: %#v", secondTool)
	}
	custom := session.SessionData.Exchanges[2].Messages[0]
	if custom.Role != "user" || custom.Content[0].Text != "remember this" {
		t.Fatalf("custom message was not preserved: %#v", custom)
	}
	for _, exchange := range session.SessionData.Exchanges {
		for _, message := range exchange.Messages {
			for _, part := range message.Content {
				if part.Text == "discarded branch" {
					t.Fatal("inactive Pi branch was exported")
				}
			}
		}
	}
}

func TestWatcherFireDebouncesToTheFinalWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("{\"type\":\"session\",\"id\":\"session-1\",\"timestamp\":\"2026-08-28T00:00:00Z\",\"cwd\":\"/work/project\"}\n{\"type\":\"message\",\"id\":\"user-1\",\"parentId\":null,\"timestamp\":\"2026-08-28T00:00:01Z\",\"message\":{\"role\":\"user\",\"content\":\"first\"}}\n")
	callbacks := make(chan *spi.AgentChatSession, 2)
	watcher := NewWatcher("/work/project", false, func(session *spi.AgentChatSession) { callbacks <- session })
	watcher.fire(path)
	time.Sleep(25 * time.Millisecond)
	write("{\"type\":\"session\",\"id\":\"session-1\",\"timestamp\":\"2026-08-28T00:00:00Z\",\"cwd\":\"/work/project\"}\n{\"type\":\"message\",\"id\":\"user-1\",\"parentId\":null,\"timestamp\":\"2026-08-28T00:00:01Z\",\"message\":{\"role\":\"user\",\"content\":\"final\"}}\n")
	watcher.fire(path)

	select {
	case session := <-callbacks:
		if got := session.SessionData.Exchanges[0].Messages[0].Content[0].Text; got != "final" {
			t.Fatalf("callback read %q, want final write", got)
		}
	case <-time.After(time.Second):
		t.Fatal("watcher did not invoke callback")
	}
	select {
	case <-callbacks:
		t.Fatal("watcher invoked callback more than once for debounced writes")
	case <-time.After(350 * time.Millisecond):
	}
}

func TestFindPiSessionsFiltersByHeaderCWD(t *testing.T) {
	home := t.TempDir()
	previous := piUserHomeDir
	piUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { piUserHomeDir = previous })
	root, err := piSessionsRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := "{\"type\":\"session\",\"id\":\"right\",\"timestamp\":\"2026-08-28T00:00:00Z\",\"cwd\":\"/work/right\"}\n"
	if err := os.WriteFile(filepath.Join(root, "project", "right.jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "project", "bad.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, err := findPiSessions("/work/right", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "right" {
		t.Fatalf("sessions = %#v", sessions)
	}
}
