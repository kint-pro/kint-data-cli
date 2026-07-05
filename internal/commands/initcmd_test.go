package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func fakeGitLFS(t *testing.T, versionOutput string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test is unix-only")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"lfs\" ] && [ \"$2\" = \"version\" ]; then echo '" + versionOutput + "'; exit 0; fi\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestCheckGitLFSAcceptsV3(t *testing.T) {
	fakeGitLFS(t, "git-lfs/3.7.1 (GitHub; darwin arm64; go 1.25.3)")
	if err := checkGitLFS(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckGitLFSRejectsV2(t *testing.T) {
	fakeGitLFS(t, "git-lfs/2.13.3 (GitHub; darwin amd64; go 1.16.2)")
	if err := checkGitLFS(); err == nil {
		t.Fatal("expected version error for git-lfs 2.x")
	}
}

func TestCheckGitLFSMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test is unix-only")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"lfs\" ]; then exit 1; fi\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if err := checkGitLFS(); err == nil {
		t.Fatal("expected error when git-lfs missing")
	}
}
