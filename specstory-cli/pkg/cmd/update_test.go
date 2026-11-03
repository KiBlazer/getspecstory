package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUpdateReadsVersionFromClonedRepository(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	run("init", "-b", "release")
	if err := os.MkdirAll(filepath.Join(repo, "specstory-cli"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "specstory-cli", "VERSION"), []byte("v1.13.2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "fixture")
	command := CreateUpdateCommand("v1.13.2")
	command.SetArgs([]string{"--repo", repo, "--install-dir", t.TempDir()})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
}

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
