package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/kint-pro/kint-data-cli/internal/auth"
	"github.com/kint-pro/kint-data-cli/internal/config"
	"github.com/kint-pro/kint-data-cli/internal/storage"
)

type check struct {
	name string
	run  func() (string, error)
}

func CmdDoctor(version string) {
	ctx := context.Background()
	var cfg *config.Config
	failed := false

	checks := []check{
		{"binary", func() (string, error) {
			return "kint-data " + version, nil
		}},
		{"git", func() (string, error) {
			out, err := exec.Command("git", "--version").Output()
			if err != nil {
				return "", fmt.Errorf("git not found")
			}
			return string(out[:len(out)-1]), nil
		}},
		{"git-lfs", func() (string, error) {
			if err := checkGitLFS(); err != nil {
				return "", err
			}
			out, _ := exec.Command("git", "lfs", "version").Output()
			return string(out[:len(out)-1]), nil
		}},
		{"repo config", func() (string, error) {
			var err error
			cfg, err = config.Load()
			if err != nil {
				return "", err
			}
			if config.Get("lfs.standalonetransferagent") != "kintdata" {
				return "", fmt.Errorf("transfer agent not registered - run `kint-data init`")
			}
			if registered := config.Get("kintdata.registereddriveid"); registered != cfg.DriveID {
				return "", fmt.Errorf("drive ID changed (registered %s, configured %s) - verify .kintdata, then re-run `kint-data init`", registered, cfg.DriveID)
			}
			return fmt.Sprintf("drive %s, root %s", cfg.DriveID, cfg.RootPath), nil
		}},
		{"authentication", func() (string, error) {
			if cfg == nil {
				return "", fmt.Errorf("skipped (repo config failed)")
			}
			client, err := auth.NewClient(cfg.TenantID, cfg.ClientID)
			if err != nil {
				return "", err
			}
			if _, err := auth.TokenSilent(ctx, client); err != nil {
				return "", err
			}
			return "signed in as " + auth.AccountName(ctx, client), nil
		}},
		{"drive reachability", func() (string, error) {
			if cfg == nil {
				return "", fmt.Errorf("skipped (repo config failed)")
			}
			client, err := auth.NewClient(cfg.TenantID, cfg.ClientID)
			if err != nil {
				return "", err
			}
			store := storage.New(auth.TokenSource(client), cfg.DriveID, cfg.RootPath, os.Stderr)
			info, err := store.Drive(ctx)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("drive %q at %s", info.Name, info.WebURL), nil
		}},
	}

	for _, c := range checks {
		detail, err := c.run()
		if err != nil {
			fmt.Printf("✗ %-18s %v\n", c.name, err)
			failed = true
		} else {
			fmt.Printf("✓ %-18s %s\n", c.name, detail)
		}
	}
	if failed {
		os.Exit(1)
	}
}
