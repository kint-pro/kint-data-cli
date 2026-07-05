package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kint-pro/kint-data-cli/internal/storage"
)

const oid = "3f9ab2c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2"

func fakeGraph(t *testing.T, handler http.HandlerFunc) *storage.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := storage.New(func(ctx context.Context) (string, error) { return "tok", nil }, "D", "/store", io.Discard)
	c.BaseURL = server.URL + "/v1.0"
	return c
}

func decodeLines(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var msgs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stdout line is not JSON: %q", line)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func TestRunTerminateExitsCleanly(t *testing.T) {
	var out bytes.Buffer
	err := Run(strings.NewReader(`{"event":"terminate"}`+"\n"), &out, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("terminate must not produce output, got %s", out.String())
	}
}

func TestRunInitWithoutRepoFailsWithRemediation(t *testing.T) {
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	input := `{"event":"init","operation":"download","remote":"origin","concurrent":true,"concurrenttransfers":8}` + "\n" +
		`{"event":"terminate"}` + "\n"
	if err := Run(strings.NewReader(input), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	msgs := decodeLines(t, &out)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 response, got %d", len(msgs))
	}
	errObj, ok := msgs[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected init error, got %v", msgs[0])
	}
	if !strings.Contains(errObj["message"].(string), "kint-data init") {
		t.Fatalf("unexpected message: %v", errObj["message"])
	}
}

func TestUploadSuccessEmitsProgressAndComplete(t *testing.T) {
	store := fakeGraph(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			w.WriteHeader(404)
		case r.Method == "POST":
			w.WriteHeader(201)
		case r.Method == "PUT":
			w.WriteHeader(201)
		}
	})
	var out bytes.Buffer
	a := &Agent{store: store, out: bufio.NewWriter(&out), log: io.Discard}
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, []byte("hello"), 0o600)
	a.handleUpload(message{Event: "upload", Oid: oid, Size: 5, Path: file})
	msgs := decodeLines(t, &out)
	if len(msgs) < 2 {
		t.Fatalf("expected progress + complete, got %v", msgs)
	}
	if msgs[0]["event"] != "progress" || msgs[0]["bytesSoFar"].(float64) != 5 {
		t.Fatalf("bad progress event: %v", msgs[0])
	}
	last := msgs[len(msgs)-1]
	if last["event"] != "complete" || last["oid"] != oid || last["error"] != nil {
		t.Fatalf("bad complete event: %v", last)
	}
}

func TestUploadFailureReportsErrorAndAgentContinues(t *testing.T) {
	store := fakeGraph(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			w.WriteHeader(404)
		case r.Method == "POST":
			w.WriteHeader(201)
		case r.Method == "PUT":
			w.WriteHeader(403)
		}
	})
	var out bytes.Buffer
	a := &Agent{store: store, out: bufio.NewWriter(&out), log: io.Discard}
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, []byte("hello"), 0o600)
	a.handleUpload(message{Event: "upload", Oid: oid, Size: 5, Path: file})
	msgs := decodeLines(t, &out)
	last := msgs[len(msgs)-1]
	errObj, ok := last["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error in complete, got %v", last)
	}
	if errObj["code"].(float64) != 403 {
		t.Fatalf("expected code 403, got %v", errObj["code"])
	}
}

func TestDownloadSuccessDeliversPath(t *testing.T) {
	store := fakeGraph(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("payload"))
	})
	tmpDir := t.TempDir()
	var out bytes.Buffer
	a := &Agent{store: store, tmpDir: tmpDir, out: bufio.NewWriter(&out), log: io.Discard}
	a.handleDownload(message{Event: "download", Oid: oid, Size: 7})
	msgs := decodeLines(t, &out)
	last := msgs[len(msgs)-1]
	path, _ := last["path"].(string)
	if last["event"] != "complete" || path == "" {
		t.Fatalf("bad complete: %v", last)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "payload" {
		t.Fatalf("delivered file wrong: %v %q", err, data)
	}
}

func TestDownloadMissingOidReportsError(t *testing.T) {
	store := fakeGraph(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})
	var out bytes.Buffer
	a := &Agent{store: store, tmpDir: t.TempDir(), out: bufio.NewWriter(&out), log: io.Discard}
	a.handleDownload(message{Event: "download", Oid: oid, Size: 7})
	msgs := decodeLines(t, &out)
	errObj, ok := msgs[len(msgs)-1]["error"].(map[string]any)
	if !ok || !strings.Contains(errObj["message"].(string), oid) {
		t.Fatalf("expected error naming oid, got %v", msgs)
	}
}

func TestStdoutPurity(t *testing.T) {
	var out, log bytes.Buffer
	input := `{"event":"unknown-event"}` + "\n" + `not json at all` + "\n" + `{"event":"terminate"}` + "\n"
	if err := Run(strings.NewReader(input), &out, &log); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("diagnostics leaked to stdout: %s", out.String())
	}
	if !strings.Contains(log.String(), "unknown event") || !strings.Contains(log.String(), "invalid message") {
		t.Fatalf("expected diagnostics on stderr, got: %s", log.String())
	}
}

func TestInvalidOidRejectedWithoutPanic(t *testing.T) {
	store := fakeGraph(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	var out bytes.Buffer
	a := &Agent{store: store, out: bufio.NewWriter(&out), log: io.Discard}
	a.handleUpload(message{Event: "upload", Oid: "no", Size: 1, Path: "/nonexistent"})
	msgs := decodeLines(t, &out)
	errObj, ok := msgs[len(msgs)-1]["error"].(map[string]any)
	if !ok || !strings.Contains(errObj["message"].(string), "invalid oid") {
		t.Fatalf("expected invalid-oid error, got %v", msgs)
	}
}
