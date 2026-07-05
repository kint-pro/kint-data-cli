package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/kint-pro/kint-data-cli/internal/auth"
	"github.com/kint-pro/kint-data-cli/internal/config"
	"github.com/kint-pro/kint-data-cli/internal/storage"
)

var oidPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type message struct {
	Event     string `json:"event"`
	Operation string `json:"operation,omitempty"`
	Oid       string `json:"oid,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Path      string `json:"path,omitempty"`
}

type transferError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	Event string         `json:"event,omitempty"`
	Oid   string         `json:"oid,omitempty"`
	Path  string         `json:"path,omitempty"`
	Error *transferError `json:"error,omitempty"`
}

type progressEvent struct {
	Event          string `json:"event"`
	Oid            string `json:"oid"`
	BytesSoFar     int64  `json:"bytesSoFar"`
	BytesSinceLast int64  `json:"bytesSinceLast"`
}

type Agent struct {
	store  *storage.Client
	tmpDir string
	out    *bufio.Writer
	log    io.Writer
}

func Run(stdin io.Reader, stdout, log io.Writer) error {
	a := &Agent{out: bufio.NewWriter(stdout), log: log}
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg message
		if err := json.Unmarshal(line, &msg); err != nil {
			fmt.Fprintf(log, "invalid message: %v\n", err)
			continue
		}
		switch msg.Event {
		case "init":
			a.handleInit()
		case "upload":
			a.handleUpload(msg)
		case "download":
			a.handleDownload(msg)
		case "terminate":
			return nil
		default:
			fmt.Fprintf(log, "unknown event: %s\n", msg.Event)
		}
	}
	return scanner.Err()
}

func (a *Agent) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(a.log, "marshal failure: %v\n", err)
		return
	}
	a.out.Write(data)
	a.out.WriteByte('\n')
	if err := a.out.Flush(); err != nil {
		fmt.Fprintf(a.log, "stdout write failed: %v\n", err)
	}
}

func (a *Agent) handleInit() {
	if err := a.initialize(); err != nil {
		a.send(map[string]any{"error": transferError{Code: 2, Message: err.Error()}})
		return
	}
	a.send(struct{}{})
}

func (a *Agent) initialize() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	registered := config.Get("kintdata.registereddriveid")
	if registered == "" {
		return fmt.Errorf("transfer agent not registered - run `kint-data init`")
	}
	if registered != cfg.DriveID {
		return fmt.Errorf("drive ID changed (registered %s, configured %s) - verify .kintdata, then re-run `kint-data init`", registered, cfg.DriveID)
	}
	client, err := auth.NewClient(cfg.TenantID, cfg.ClientID)
	if err != nil {
		return err
	}
	if _, err := auth.TokenSilent(context.Background(), client); err != nil {
		return err
	}
	gitDir, err := config.GitDir()
	if err != nil {
		return err
	}
	a.tmpDir = filepath.Join(gitDir, "lfs", "tmp")
	if err := os.MkdirAll(a.tmpDir, 0o700); err != nil {
		return err
	}
	cleanupStale(a.tmpDir, a.log)
	a.store = storage.New(auth.TokenSource(client), cfg.DriveID, cfg.RootPath, a.log)
	return nil
}

func cleanupStale(dir string, log io.Writer) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) &&
			len(e.Name()) > 9 && e.Name()[:9] == "kintdata-" {
			if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
				fmt.Fprintf(log, "removed stale temp file %s\n", e.Name())
			}
		}
	}
}

func (a *Agent) progressFunc(oid string) func(int64) {
	var soFar int64
	return func(n int64) {
		soFar += n
		a.send(progressEvent{Event: "progress", Oid: oid, BytesSoFar: soFar, BytesSinceLast: n})
	}
}

func errorCode(err error) int {
	var gerr *storage.GraphError
	if errors.As(err, &gerr) && gerr.StatusCode >= 400 {
		return gerr.StatusCode
	}
	return 1
}

func (a *Agent) transferReady(oid string) bool {
	if a.store == nil {
		a.send(response{Event: "complete", Oid: oid, Error: &transferError{Code: 2, Message: "agent not initialized"}})
		return false
	}
	if !oidPattern.MatchString(oid) {
		a.send(response{Event: "complete", Oid: oid, Error: &transferError{Code: 1, Message: fmt.Sprintf("invalid oid %q - expected 64 hex chars", oid)}})
		return false
	}
	return true
}

func (a *Agent) handleUpload(msg message) {
	if !a.transferReady(msg.Oid) {
		return
	}
	err := a.store.Upload(context.Background(), msg.Oid, msg.Size, msg.Path, a.progressFunc(msg.Oid))
	if err != nil {
		fmt.Fprintf(a.log, "upload %s failed: %v\n", msg.Oid, err)
		a.send(response{Event: "complete", Oid: msg.Oid, Error: &transferError{Code: errorCode(err), Message: err.Error()}})
		return
	}
	a.send(response{Event: "complete", Oid: msg.Oid})
}

func (a *Agent) handleDownload(msg message) {
	if !a.transferReady(msg.Oid) {
		return
	}
	path, err := a.store.Download(context.Background(), msg.Oid, a.tmpDir, a.progressFunc(msg.Oid))
	if err != nil {
		fmt.Fprintf(a.log, "download %s failed: %v\n", msg.Oid, err)
		a.send(response{Event: "complete", Oid: msg.Oid, Error: &transferError{Code: errorCode(err), Message: err.Error()}})
		return
	}
	a.send(response{Event: "complete", Oid: msg.Oid, Path: path})
}
