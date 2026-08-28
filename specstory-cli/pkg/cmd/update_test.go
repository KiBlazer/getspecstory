package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestGitHubVersionURL(t *testing.T) {
	got, err := githubVersionURL("https://github.com/KiBlazer/getspecstory.git", "release")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://raw.githubusercontent.com/KiBlazer/getspecstory/release/specstory-cli/VERSION"
	if got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

func TestFetchRemoteVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v1.13.2\n")
	}))
	defer server.Close()

	version, err := fetchRemoteVersion(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if version != "v1.13.2" {
		t.Fatalf("version = %q", version)
	}
}

func TestUpdateSkipsDownloadWhenVersionMatches(t *testing.T) {
	originalClient := updateHTTPClient
	t.Cleanup(func() { updateHTTPClient = originalClient })

	requestCount := 0
	updateHTTPClient = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		if request.URL.String() != "https://raw.githubusercontent.com/KiBlazer/getspecstory/release/specstory-cli/VERSION" {
			t.Fatalf("version URL = %q", request.URL)
		}
		response := &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader("v1.13.2\n")),
			Header:     make(http.Header),
		}
		return response, nil
	})}

	command := CreateUpdateCommand("v1.13.2")
	command.SetArgs([]string{"--install-dir", t.TempDir()})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 {
		t.Fatalf("version requests = %d, want 1", requestCount)
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
