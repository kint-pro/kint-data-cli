package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/gofrs/flock"
)

type fakeMarshaler struct{ data []byte }

func (f fakeMarshaler) Marshal() ([]byte, error) { return f.data, nil }

type fakeUnmarshaler struct{ got []byte }

func (f *fakeUnmarshaler) Unmarshal(b []byte) error { f.got = append([]byte(nil), b...); return nil }

func testCache(t *testing.T) *diskCache {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "kint-data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "msal_cache.json")
	return &diskCache{path: p, lock: flock.New(p + ".lock")}
}

func TestCacheRoundTripAndPermissions(t *testing.T) {
	c := testCache(t)
	payload := []byte(`{"account":"x"}`)
	if err := c.Export(context.Background(), fakeMarshaler{payload}, cache.ExportHints{}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(c.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("cache file perms %v, want 0600", info.Mode().Perm())
		}
		dirInfo, _ := os.Stat(filepath.Dir(c.path))
		if dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("cache dir perms %v, want 0700", dirInfo.Mode().Perm())
		}
	}
	var u fakeUnmarshaler
	if err := c.Replace(context.Background(), &u, cache.ReplaceHints{}); err != nil {
		t.Fatal(err)
	}
	if string(u.got) != string(payload) {
		t.Fatal("round trip mismatch")
	}
}

func TestCacheReplaceWithoutFileIsNoop(t *testing.T) {
	c := testCache(t)
	var u fakeUnmarshaler
	if err := c.Replace(context.Background(), &u, cache.ReplaceHints{}); err != nil {
		t.Fatal(err)
	}
	if u.got != nil {
		t.Fatal("expected no unmarshal without cache file")
	}
}

func TestCacheConcurrentExportsStayValid(t *testing.T) {
	c := testCache(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			doc, _ := json.Marshal(map[string]int{"writer": i, "field": i * 7})
			if err := c.Export(context.Background(), fakeMarshaler{doc}, cache.ExportHints{}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	data, err := os.ReadFile(c.path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]int
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("cache corrupted after concurrent writes: %v (%s)", err, data)
	}
}

func TestSilentFailureNamesLogin(t *testing.T) {
	err := fmt.Errorf("%w", ErrNotLoggedIn)
	if got := err.Error(); got != "no valid session - run `kint-data login`" {
		t.Fatalf("unexpected message: %s", got)
	}
}
