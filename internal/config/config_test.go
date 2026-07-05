package config

import (
	"os/exec"
	"strings"
	"testing"
)

func setupRepo(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	if out, err := exec.Command("git", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s", out)
	}
}

func TestLoadFailsOutsideRepo(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := Load(); err == nil {
		t.Fatal("expected error outside git repo")
	}
}

func TestLoadReportsMissingKeys(t *testing.T) {
	setupRepo(t)
	_, err := Load()
	if err == nil {
		t.Fatal("expected missing-keys error")
	}
	for _, k := range []string{"kintdata.tenantid", "kintdata.clientid", "kintdata.driveid", "kintdata.rootpath"} {
		if !strings.Contains(err.Error(), k) {
			t.Fatalf("error does not name %s: %v", k, err)
		}
	}
}

func TestLoadReadsSharedFile(t *testing.T) {
	setupRepo(t)
	for k, v := range map[string]string{
		"kintdata.tenantid": "T", "kintdata.clientid": "C",
		"kintdata.driveid": "D", "kintdata.rootpath": "/lfs",
	} {
		exec.Command("git", "config", "-f", ".kintdata", k, v).Run()
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TenantID != "T" || cfg.ClientID != "C" || cfg.DriveID != "D" || cfg.RootPath != "/lfs" {
		t.Fatalf("bad config: %+v", cfg)
	}
}

func TestLocalOverrideWins(t *testing.T) {
	setupRepo(t)
	for k, v := range map[string]string{
		"kintdata.tenantid": "T", "kintdata.clientid": "C",
		"kintdata.driveid": "shared-drive", "kintdata.rootpath": "/lfs",
	} {
		exec.Command("git", "config", "-f", ".kintdata", k, v).Run()
	}
	exec.Command("git", "config", "--local", "kintdata.driveid", "local-drive").Run()
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DriveID != "local-drive" {
		t.Fatalf("local override lost: %s", cfg.DriveID)
	}
}
