package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	simpleUploadLimit = 4 << 20
	ChunkSize         = 32 * 320 * 1024
	maxAttempts       = 5
	maxRetryDelay     = time.Minute
)

type GraphError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *GraphError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("graph %d %s: %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("graph %d: %s", e.StatusCode, e.Message)
}

type Client struct {
	HTTP       *http.Client
	Token      func(ctx context.Context) (string, error)
	BaseURL    string
	DriveID    string
	RootPath   string
	Log        io.Writer
	ChunkBytes int64
	RetryBase  time.Duration
	ensured    map[string]bool
}

func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = time.Minute
	return &http.Client{Transport: transport}
}

func New(token func(ctx context.Context) (string, error), driveID, rootPath string, log io.Writer) *Client {
	return &Client{
		HTTP:       newHTTPClient(),
		Token:      token,
		BaseURL:    "https://graph.microsoft.com/v1.0",
		DriveID:    driveID,
		RootPath:   "/" + strings.Trim(rootPath, "/"),
		Log:        log,
		ChunkBytes: ChunkSize,
		RetryBase:  time.Second,
		ensured:    map[string]bool{},
	}
}

func (c *Client) ObjectPath(oid string) string {
	return path.Join(c.RootPath, "objects", oid[0:2], oid[2:4], oid)
}

func (c *Client) itemURL(itemPath, suffix string) string {
	escaped := (&url.URL{Path: itemPath}).EscapedPath()
	return fmt.Sprintf("%s/drives/%s/root:%s%s", c.BaseURL, c.DriveID, escaped, suffix)
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable url)"
	}
	u.RawQuery = ""
	return u.String()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) do(ctx context.Context, method, rawURL string, body []byte, headers map[string]string) (*http.Response, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if strings.HasPrefix(rawURL, c.BaseURL) {
			token, err := c.Token(ctx)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := c.HTTP.Do(req)
		delay := time.Duration(1<<(attempt-1)) * c.RetryBase
		if err != nil {
			var uerr *url.Error
			if errors.As(err, &uerr) {
				lastErr = fmt.Errorf("%s %s: %w", uerr.Op, redactURL(rawURL), uerr.Err)
			} else {
				lastErr = err
			}
		} else if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = &GraphError{StatusCode: resp.StatusCode, Message: resp.Status}
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil {
					delay = time.Duration(secs) * time.Second
				}
			}
			resp.Body.Close()
		} else {
			return resp, nil
		}
		if attempt < maxAttempts {
			if delay > maxRetryDelay {
				delay = maxRetryDelay
			}
			if err := sleepCtx(ctx, delay); err != nil {
				return nil, err
			}
		}
	}
	return nil, fmt.Errorf("giving up after %d attempts: %w", maxAttempts, lastErr)
}

func graphError(resp *http.Response) error {
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &parsed) == nil && parsed.Error.Code != "" {
		return &GraphError{StatusCode: resp.StatusCode, Code: parsed.Error.Code, Message: parsed.Error.Message}
	}
	return &GraphError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(data))}
}

func drainClose(resp *http.Response) {
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

func (c *Client) Stat(ctx context.Context, itemPath string) (int64, bool, error) {
	resp, err := c.do(ctx, "GET", c.itemURL(itemPath, "?$select=size"), nil, nil)
	if err != nil {
		return 0, false, err
	}
	if resp.StatusCode == 404 {
		resp.Body.Close()
		return 0, false, nil
	}
	if resp.StatusCode != 200 {
		return 0, false, graphError(resp)
	}
	defer resp.Body.Close()
	var item struct {
		Size int64 `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return 0, false, err
	}
	return item.Size, true, nil
}

func (c *Client) ensureFolders(ctx context.Context, itemPath string) error {
	parent := path.Dir(itemPath)
	if c.ensured[parent] {
		return nil
	}
	segments := strings.Split(strings.Trim(parent, "/"), "/")
	current := ""
	for _, seg := range segments {
		next := current + "/" + seg
		if !c.ensured[next] {
			if err := c.createFolder(ctx, current, seg); err != nil {
				return err
			}
			c.ensured[next] = true
		}
		current = next
	}
	return nil
}

func (c *Client) createFolder(ctx context.Context, parent, name string) error {
	var childrenURL string
	if parent == "" {
		childrenURL = fmt.Sprintf("%s/drives/%s/root/children", c.BaseURL, c.DriveID)
	} else {
		childrenURL = c.itemURL(parent, ":/children")
	}
	body, _ := json.Marshal(map[string]any{
		"name":                              name,
		"folder":                            map[string]any{},
		"@microsoft.graph.conflictBehavior": "fail",
	})
	resp, err := c.do(ctx, "POST", childrenURL, body, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	if resp.StatusCode == 201 || resp.StatusCode == 409 {
		drainClose(resp)
		return nil
	}
	return graphError(resp)
}

func (c *Client) Upload(ctx context.Context, oid string, size int64, filePath string, progress func(int64)) error {
	itemPath := c.ObjectPath(oid)
	remoteSize, exists, err := c.Stat(ctx, itemPath)
	if err != nil {
		return err
	}
	replace := false
	if exists {
		if remoteSize == size {
			progress(size)
			return nil
		}
		fmt.Fprintf(c.Log, "warning: remote object %s has size %d, expected %d - repairing by replace\n", oid, remoteSize, size)
		replace = true
	}
	if err := c.ensureFolders(ctx, itemPath); err != nil {
		return err
	}
	if size <= simpleUploadLimit {
		return c.uploadSmall(ctx, itemPath, size, filePath, replace, progress)
	}
	return c.uploadSession(ctx, itemPath, size, filePath, replace, progress)
}

func conflictBehavior(replace bool) string {
	if replace {
		return "replace"
	}
	return "fail"
}

func (c *Client) conflictRecheck(ctx context.Context, itemPath string, size int64) error {
	remoteSize, exists, err := c.Stat(ctx, itemPath)
	if err != nil {
		return err
	}
	if exists && remoteSize == size {
		return nil
	}
	return fmt.Errorf("conflict on %s but remote size %d does not match %d", itemPath, remoteSize, size)
}

func (c *Client) uploadSmall(ctx context.Context, itemPath string, size int64, filePath string, replace bool, progress func(int64)) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	uploadURL := c.itemURL(itemPath, ":/content?@microsoft.graph.conflictBehavior="+conflictBehavior(replace))
	resp, err := c.do(ctx, "PUT", uploadURL, data, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == 200 || resp.StatusCode == 201:
		drainClose(resp)
		progress(size)
		return nil
	case resp.StatusCode == 409:
		drainClose(resp)
		if err := c.conflictRecheck(ctx, itemPath, size); err != nil {
			return err
		}
		progress(size)
		return nil
	default:
		return graphError(resp)
	}
}

func (c *Client) uploadSession(ctx context.Context, itemPath string, size int64, filePath string, replace bool, progress func(int64)) error {
	body, _ := json.Marshal(map[string]any{
		"item": map[string]any{"@microsoft.graph.conflictBehavior": conflictBehavior(replace)},
	})
	resp, err := c.do(ctx, "POST", c.itemURL(itemPath, ":/createUploadSession"), body, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return graphError(resp)
	}
	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	err = json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(session.UploadURL, "https://") {
		return fmt.Errorf("upload session URL is not https")
	}
	if err := c.uploadChunks(ctx, itemPath, size, filePath, session.UploadURL, progress); err != nil {
		cancelReq, cerr := http.NewRequestWithContext(context.WithoutCancel(ctx), "DELETE", session.UploadURL, nil)
		if cerr == nil {
			if cancelResp, cerr := c.HTTP.Do(cancelReq); cerr == nil {
				cancelResp.Body.Close()
			}
		}
		return err
	}
	return nil
}

func (c *Client) uploadChunks(ctx context.Context, itemPath string, size int64, filePath, uploadURL string, progress func(int64)) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	buf := make([]byte, c.ChunkBytes)
	var offset int64
	for offset < size {
		want := c.ChunkBytes
		if remaining := size - offset; remaining < want {
			want = remaining
		}
		n, err := io.ReadFull(file, buf[:want])
		if err != nil {
			return fmt.Errorf("local file %s truncated: read %d of %d bytes at offset %d: %w", filePath, n, want, offset, err)
		}
		chunk := buf[:n]
		end := offset + int64(n) - 1
		headers := map[string]string{
			"Content-Range": fmt.Sprintf("bytes %d-%d/%d", offset, end, size),
		}
		resp, err := c.do(ctx, "PUT", uploadURL, chunk, headers)
		if err != nil {
			return err
		}
		switch {
		case resp.StatusCode == 202 || resp.StatusCode == 200 || resp.StatusCode == 201:
			drainClose(resp)
		case resp.StatusCode == 409:
			drainClose(resp)
			if err := c.conflictRecheck(ctx, itemPath, size); err != nil {
				return err
			}
			progress(size - offset)
			return nil
		default:
			return graphError(resp)
		}
		offset += int64(n)
		progress(int64(n))
	}
	return nil
}

func (c *Client) Download(ctx context.Context, oid string, tmpDir string, progress func(int64)) (string, error) {
	itemPath := c.ObjectPath(oid)
	resp, err := c.do(ctx, "GET", c.itemURL(itemPath, ":/content"), nil, nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == 404 {
		drainClose(resp)
		return "", fmt.Errorf("object %s not found in remote store", oid)
	}
	if resp.StatusCode != 200 {
		return "", graphError(resp)
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(tmpDir, "kintdata-*")
	if err != nil {
		return "", err
	}
	if err := writeDownload(tmp, resp.Body, progress); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func writeDownload(tmp *os.File, body io.Reader, progress func(int64)) error {
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	buf := make([]byte, 1<<20)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				tmp.Close()
				return werr
			}
			progress(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			tmp.Close()
			return rerr
		}
	}
	return tmp.Close()
}

type DriveInfo struct {
	Name   string `json:"name"`
	WebURL string `json:"webUrl"`
}

func (c *Client) Drive(ctx context.Context) (*DriveInfo, error) {
	resp, err := c.do(ctx, "GET", fmt.Sprintf("%s/drives/%s?$select=name,webUrl", c.BaseURL, c.DriveID), nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, graphError(resp)
	}
	defer resp.Body.Close()
	var info DriveInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}
