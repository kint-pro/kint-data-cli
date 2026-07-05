package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
	"github.com/gofrs/flock"
)

var ErrNotLoggedIn = errors.New("no valid session - run `kint-data login`")

var Scopes = []string{"https://graph.microsoft.com/Sites.Selected"}

func CacheDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "kint-data"), nil
}

func cachePath() (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "msal_cache.json"), nil
}

type diskCache struct {
	path string
	mu   sync.Mutex
	lock *flock.Flock
}

func newDiskCache() (*diskCache, error) {
	p, err := cachePath()
	if err != nil {
		return nil, err
	}
	return &diskCache{path: p, lock: flock.New(p + ".lock")}, nil
}

func (c *diskCache) acquireLock(ctx context.Context) error {
	lockCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	locked, err := c.lock.TryLockContext(lockCtx, 50*time.Millisecond)
	if err != nil {
		return fmt.Errorf("token cache lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("token cache lock timeout")
	}
	return nil
}

func (c *diskCache) Replace(ctx context.Context, u cache.Unmarshaler, _ cache.ReplaceHints) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.acquireLock(ctx); err != nil {
		return err
	}
	defer c.lock.Unlock()
	data, err := os.ReadFile(c.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return u.Unmarshal(data)
}

func (c *diskCache) Export(ctx context.Context, m cache.Marshaler, _ cache.ExportHints) error {
	data, err := m.Marshal()
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.acquireLock(ctx); err != nil {
		return err
	}
	defer c.lock.Unlock()
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".msal_cache-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmp.Name(), c.path)
}

func NewClient(tenantID, clientID string) (public.Client, error) {
	dc, err := newDiskCache()
	if err != nil {
		return public.Client{}, err
	}
	return public.New(clientID,
		public.WithAuthority("https://login.microsoftonline.com/"+tenantID),
		public.WithCache(dc),
	)
}

func TokenSource(client public.Client) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		return TokenSilent(ctx, client)
	}
}

func TokenSilent(ctx context.Context, client public.Client) (string, error) {
	accounts, err := client.Accounts(ctx)
	if err != nil || len(accounts) == 0 {
		return "", ErrNotLoggedIn
	}
	result, err := client.AcquireTokenSilent(ctx, Scopes, public.WithSilentAccount(accounts[0]))
	if err != nil {
		return "", fmt.Errorf("%w (silent acquisition failed: %v)", ErrNotLoggedIn, err)
	}
	return result.AccessToken, nil
}

func AccountName(ctx context.Context, client public.Client) string {
	accounts, err := client.Accounts(ctx)
	if err != nil || len(accounts) == 0 {
		return ""
	}
	return accounts[0].PreferredUsername
}

func RemoveCache() error {
	p, err := cachePath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	os.Remove(p + ".lock")
	return nil
}
