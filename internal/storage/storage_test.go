package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testToken(ctx context.Context) (string, error) { return "test-token", nil }

func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	c := New(testToken, "DRIVE", "/lfs-store", io.Discard)
	c.HTTP = server.Client()
	c.BaseURL = server.URL + "/v1.0"
	c.RetryBase = time.Millisecond
	return c, server
}

func writeTempFile(t *testing.T, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "obj")
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const oid = "3f9ab2c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2"

func TestObjectPath(t *testing.T) {
	c := New(testToken, "D", "lfs-store", io.Discard)
	got := c.ObjectPath(oid)
	want := "/lfs-store/objects/3f/9a/" + oid
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestUploadSkipsExistingObject(t *testing.T) {
	var uploads atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.Path, oid):
			json.NewEncoder(w).Encode(map[string]int64{"size": 100})
		case r.Method == "PUT":
			uploads.Add(1)
			w.WriteHeader(201)
		default:
			w.WriteHeader(404)
		}
	}))
	var progressed int64
	err := c.Upload(context.Background(), oid, 100, writeTempFile(t, 100), func(n int64) { progressed += n })
	if err != nil {
		t.Fatal(err)
	}
	if uploads.Load() != 0 {
		t.Fatal("expected no upload for existing object")
	}
	if progressed != 100 {
		t.Fatalf("expected full progress, got %d", progressed)
	}
}

func TestUploadSmallCreatesFoldersAndPuts(t *testing.T) {
	var folders []string
	var putURL string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			w.WriteHeader(404)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/children"):
			var body struct {
				Name string `json:"name"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			folders = append(folders, body.Name)
			w.WriteHeader(201)
		case r.Method == "PUT":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Error("missing bearer on graph PUT")
			}
			putURL = r.URL.String()
			w.WriteHeader(201)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	err := c.Upload(context.Background(), oid, 50, writeTempFile(t, 50), func(int64) {})
	if err != nil {
		t.Fatal(err)
	}
	wantFolders := []string{"lfs-store", "objects", "3f", "9a"}
	if fmt.Sprint(folders) != fmt.Sprint(wantFolders) {
		t.Fatalf("folders %v want %v", folders, wantFolders)
	}
	if !strings.Contains(putURL, "conflictBehavior=fail") {
		t.Fatalf("expected conflictBehavior=fail, got %s", putURL)
	}
}

func TestUploadConflictWithMatchingSizeIsSuccess(t *testing.T) {
	statCalls := 0
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			statCalls++
			if statCalls == 1 {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]int64{"size": 60})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/children"):
			w.WriteHeader(409)
		case r.Method == "PUT":
			w.WriteHeader(409)
		}
	}))
	if err := c.Upload(context.Background(), oid, 60, writeTempFile(t, 60), func(int64) {}); err != nil {
		t.Fatal(err)
	}
}

func TestUploadSizeMismatchRepairsWithReplace(t *testing.T) {
	var putURL string
	var logBuf strings.Builder
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]int64{"size": 999})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/children"):
			w.WriteHeader(409)
		case r.Method == "PUT":
			putURL = r.URL.String()
			w.WriteHeader(200)
		}
	}))
	c.Log = &logBuf
	if err := c.Upload(context.Background(), oid, 70, writeTempFile(t, 70), func(int64) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(putURL, "conflictBehavior=replace") {
		t.Fatalf("expected replace, got %s", putURL)
	}
	if !strings.Contains(logBuf.String(), "warning") {
		t.Fatal("expected loud warning on repair")
	}
}

func TestUploadSessionChunksAndNoAuthHeader(t *testing.T) {
	chunk := int64(6 * 320 * 1024)
	size := 3 * chunk
	var ranges []string
	var sessionAuth []string
	var received int64
	mux := http.NewServeMux()
	var c *Client
	var server *httptest.Server
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			w.WriteHeader(404)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/children"):
			w.WriteHeader(201)
		case r.Method == "POST" && strings.Contains(r.URL.Path, "createUploadSession"):
			json.NewEncoder(w).Encode(map[string]string{"uploadUrl": server.URL + "/session/xyz"})
		case strings.HasPrefix(r.URL.Path, "/session/"):
			sessionAuth = append(sessionAuth, r.Header.Get("Authorization"))
			ranges = append(ranges, r.Header.Get("Content-Range"))
			n, _ := io.Copy(io.Discard, r.Body)
			received += n
			if received >= size {
				w.WriteHeader(201)
			} else {
				w.WriteHeader(202)
			}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	})
	server = httptest.NewTLSServer(mux)
	defer server.Close()
	c = New(testToken, "DRIVE", "/lfs-store", io.Discard)
	c.HTTP = server.Client()
	c.BaseURL = server.URL + "/v1.0"
	c.RetryBase = time.Millisecond
	c.ChunkBytes = chunk

	var progressEvents int
	err := c.Upload(context.Background(), oid, size, writeTempFile(t, int(size)), func(int64) { progressEvents++ })
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 3 {
		t.Fatalf("expected 3 chunks, got %v", ranges)
	}
	if ranges[0] != fmt.Sprintf("bytes 0-%d/%d", chunk-1, size) {
		t.Fatalf("bad first range %s", ranges[0])
	}
	for _, a := range sessionAuth {
		if a != "" {
			t.Fatal("session PUT must not carry Authorization header")
		}
	}
	if progressEvents != 3 {
		t.Fatalf("expected 3 progress events, got %d", progressEvents)
	}
}

func TestUploadSessionAbortCancelsSession(t *testing.T) {
	var deleted atomic.Bool
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			w.WriteHeader(404)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/children"):
			w.WriteHeader(201)
		case r.Method == "POST" && strings.Contains(r.URL.Path, "createUploadSession"):
			json.NewEncoder(w).Encode(map[string]string{"uploadUrl": server.URL + "/session/xyz"})
		case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/session/"):
			w.WriteHeader(400)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/session/"):
			deleted.Store(true)
			w.WriteHeader(204)
		}
	})
	server = httptest.NewTLSServer(mux)
	defer server.Close()
	c := New(testToken, "DRIVE", "/lfs-store", io.Discard)
	c.HTTP = server.Client()
	c.BaseURL = server.URL + "/v1.0"
	c.RetryBase = time.Millisecond
	c.ChunkBytes = 320 * 1024

	err := c.Upload(context.Background(), oid, 5<<20, writeTempFile(t, 5<<20), func(int64) {})
	if err == nil {
		t.Fatal("expected error")
	}
	if !deleted.Load() {
		t.Fatal("expected DELETE on upload session after failure")
	}
}

func TestRetryHonorsRetryAfter(t *testing.T) {
	attempts := 0
	start := time.Now()
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		json.NewEncoder(w).Encode(map[string]int64{"size": 10})
	}))
	if _, _, err := c.Stat(context.Background(), "/x"); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if time.Since(start) < time.Second {
		t.Fatal("Retry-After not honored")
	}
}

func TestRetryBudgetExhausted(t *testing.T) {
	attempts := 0
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(503)
	}))
	_, _, err := c.Stat(context.Background(), "/x")
	if err == nil || !strings.Contains(err.Error(), "giving up after 5 attempts") {
		t.Fatalf("expected retry exhaustion, got %v", err)
	}
	if attempts != 5 {
		t.Fatalf("expected 5 attempts, got %d", attempts)
	}
}

func TestDownloadStreamsToTempFile(t *testing.T) {
	content := make([]byte, 3<<20)
	for i := range content {
		content[i] = byte(i % 253)
	}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	tmpDir := t.TempDir()
	var progressed int64
	path, err := c.Download(context.Background(), oid, tmpDir, func(n int64) { progressed += n })
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("temp file perms %v, want 0600", info.Mode().Perm())
		}
	}
	if !strings.HasPrefix(filepath.Base(path), "kintdata-") {
		t.Fatalf("temp file name %s lacks kintdata- prefix", path)
	}
	got, _ := os.ReadFile(path)
	if len(got) != len(content) || progressed != int64(len(content)) {
		t.Fatal("content or progress mismatch")
	}
}

func TestDownloadMissingObject(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	_, err := c.Download(context.Background(), oid, t.TempDir(), func(int64) {})
	if err == nil || !strings.Contains(err.Error(), oid) {
		t.Fatalf("expected missing-oid error, got %v", err)
	}
	entries, _ := os.ReadDir(t.TempDir())
	if len(entries) != 0 {
		t.Fatal("no temp file must remain")
	}
}

func TestUploadFailsOnTruncatedLocalFile(t *testing.T) {
	var deleted atomic.Bool
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			w.WriteHeader(404)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/children"):
			w.WriteHeader(201)
		case r.Method == "POST" && strings.Contains(r.URL.Path, "createUploadSession"):
			json.NewEncoder(w).Encode(map[string]string{"uploadUrl": server.URL + "/session/xyz"})
		case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/session/"):
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(202)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/session/"):
			deleted.Store(true)
			w.WriteHeader(204)
		}
	})
	server = httptest.NewTLSServer(mux)
	defer server.Close()
	c := New(testToken, "DRIVE", "/lfs-store", io.Discard)
	c.HTTP = server.Client()
	c.BaseURL = server.URL + "/v1.0"
	c.RetryBase = time.Millisecond

	err := c.Upload(context.Background(), oid, 6<<20, writeTempFile(t, 1<<20), func(int64) {})
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected truncation error, got %v", err)
	}
	if !deleted.Load() {
		t.Fatal("expected session cancel after truncation")
	}
}
