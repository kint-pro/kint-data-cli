package config

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var driveIDPattern = regexp.MustCompile(`^[A-Za-z0-9!_-]+$`)

type Config struct {
	TenantID string
	ClientID string
	DriveID  string
	RootPath string
}

var keys = []string{"kintdata.tenantid", "kintdata.clientid", "kintdata.driveid", "kintdata.rootpath"}

func RepoTopLevel() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repository")
	}
	return strings.TrimSpace(string(out)), nil
}

func GitDir() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repository")
	}
	return strings.TrimSpace(string(out)), nil
}

func gitConfigGet(args ...string) string {
	out, err := exec.Command("git", append([]string{"config"}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func Get(key string) string {
	if v := gitConfigGet("--local", "--get", key); v != "" {
		return v
	}
	top, err := RepoTopLevel()
	if err != nil {
		return ""
	}
	return gitConfigGet("-f", top+"/.kintdata", "--get", key)
}

func Load() (*Config, error) {
	values := make(map[string]string, len(keys))
	var missing []string
	for _, k := range keys {
		v := Get(k)
		if v == "" {
			missing = append(missing, k)
		}
		values[k] = v
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing config keys %s - run `kint-data init` (see README for first-time setup)", strings.Join(missing, ", "))
	}
	if !driveIDPattern.MatchString(values["kintdata.driveid"]) {
		return nil, fmt.Errorf("invalid kintdata.driveid %q", values["kintdata.driveid"])
	}
	if strings.ContainsAny(values["kintdata.rootpath"], ":?#") {
		return nil, fmt.Errorf("invalid kintdata.rootpath %q - must not contain ':', '?' or '#'", values["kintdata.rootpath"])
	}
	return &Config{
		TenantID: values["kintdata.tenantid"],
		ClientID: values["kintdata.clientid"],
		DriveID:  values["kintdata.driveid"],
		RootPath: values["kintdata.rootpath"],
	}, nil
}
