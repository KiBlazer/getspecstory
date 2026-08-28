package utils

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLatestForkVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v1.13.2\n")
	}))
	defer server.Close()

	originalURL := forkVersionURL
	forkVersionURL = server.URL
	t.Cleanup(func() { forkVersionURL = originalURL })

	version, err := latestForkVersion()
	if err != nil {
		t.Fatal(err)
	}
	if version != "v1.13.2" {
		t.Fatalf("version = %q, want v1.13.2", version)
	}
}
