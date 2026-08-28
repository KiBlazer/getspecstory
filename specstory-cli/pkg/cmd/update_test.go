package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallBuiltBinaryAtomicallyReplacesDestination(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "built-specstory")
	dstPath := filepath.Join(dir, "specstory")
	if err := os.WriteFile(srcPath, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstPath, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	pendingPath, err := installBuiltBinary(srcPath, dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if pendingPath != "" {
		t.Fatalf("pending path = %q, want empty", pendingPath)
	}
	data, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new binary" {
		t.Fatalf("installed contents = %q, want new binary", data)
	}
	if _, err := os.Stat(dstPath + ".new"); !os.IsNotExist(err) {
		t.Fatalf("staging file remains after successful update: %v", err)
	}
}
