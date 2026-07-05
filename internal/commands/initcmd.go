package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kint-pro/kint-data-cli/internal/config"
)

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func checkGitLFS() error {
	out, err := exec.Command("git", "lfs", "version").Output()
	if err != nil {
		return fmt.Errorf("git-lfs is not installed - install it first (brew install git-lfs)")
	}
	version := string(out)
	slash := strings.Index(version, "/")
	if slash < 0 {
		return fmt.Errorf("cannot parse git-lfs version: %s", version)
	}
	major, err := strconv.Atoi(strings.SplitN(version[slash+1:], ".", 2)[0])
	if err != nil || major < 3 {
		return fmt.Errorf("git-lfs >= 3.0 required, found: %s", strings.TrimSpace(version))
	}
	return nil
}

func CmdInit(tenant, client, remote string, yes bool) {
	top, err := config.RepoTopLevel()
	if err != nil {
		fail(err)
	}
	if err := checkGitLFS(); err != nil {
		fail(err)
	}

	if remote != "" {
		if tenant == "" || client == "" {
			fail(fmt.Errorf("--remote requires --tenant and --client"))
		}
		driveID, rootPath, found := strings.Cut(remote, ":")
		if !found || driveID == "" || rootPath == "" {
			fail(fmt.Errorf("--remote must be <drive-id>:<root-path>, e.g. b!abc123:/lfs-store"))
		}
		sharedConfig := filepath.Join(top, ".kintdata")
		for key, value := range map[string]string{
			"kintdata.tenantid": tenant,
			"kintdata.clientid": client,
			"kintdata.driveid":  driveID,
			"kintdata.rootpath": rootPath,
		} {
			if out, err := git("config", "-f", sharedConfig, key, value); err != nil {
				fail(fmt.Errorf("writing %s: %s", key, out))
			}
		}
		fmt.Println("wrote kintdata.* keys to .kintdata - commit it so teammates get them")
	}

	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}

	registered := config.Get("kintdata.registereddriveid")
	if registered != "" && registered != cfg.DriveID && !yes {
		fail(fmt.Errorf("drive ID changed: previously registered %s, .kintdata now says %s - verify this is intended, then re-run with --yes", registered, cfg.DriveID))
	}

	if out, err := git("lfs", "install", "--local"); err != nil {
		fail(fmt.Errorf("git lfs install: %s", out))
	}

	binary, err := os.Executable()
	if err != nil {
		fail(err)
	}
	binary = filepath.ToSlash(binary)

	for key, value := range map[string]string{
		"lfs.standalonetransferagent":      "kintdata",
		"lfs.customtransfer.kintdata.path": binary,
		"lfs.customtransfer.kintdata.args": "lfs-agent",
		"kintdata.registereddriveid":       cfg.DriveID,
	} {
		if out, err := git("config", "--local", key, value); err != nil {
			fail(fmt.Errorf("writing %s: %s", key, out))
		}
	}

	fmt.Printf("repository wired to kint-data (drive %s, root %s)\n", cfg.DriveID, cfg.RootPath)
	fmt.Println("next: kint-data login (once per machine), then git lfs pull / git push as usual")
}
