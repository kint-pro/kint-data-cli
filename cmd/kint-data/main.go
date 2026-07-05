package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kint-pro/kint-data-cli/internal/agent"
	"github.com/kint-pro/kint-data-cli/internal/commands"
)

var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `kint-data - team data sharing via git-lfs and Kint OneDrive

Usage:
  kint-data <command> [flags]

Commands:
  init       Wire the current git repo to the kint-data transfer agent
  login      Sign in with your Kint account (device code)
  logout     Remove the local session
  doctor     Verify installation, auth, and drive reachability
  lfs-agent  (internal) git-lfs custom transfer agent

Flags:
  --version  Show version
  --help     Show this help

`)
}

func aliasTip() {
	fmt.Fprintf(os.Stderr, `Tip: Create a short alias "kd" for kint-data:

  macOS/Linux (zsh):  echo 'alias kd="kint-data"' >> ~/.zshrc && source ~/.zshrc
  macOS/Linux (bash): echo 'alias kd="kint-data"' >> ~/.bashrc && source ~/.bashrc
  Windows (PS):       Set-Alias -Name kd -Value kint-data -Scope Global

`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		aliasTip()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "--version", "-v":
		fmt.Printf("kint-data %s\n", version)
	case "--help", "-h":
		usage()
	case "init":
		fs := flag.NewFlagSet("init", flag.ExitOnError)
		tenant := fs.String("tenant", "", "Entra tenant ID (first-time setup)")
		client := fs.String("client", "", "Entra app client ID (first-time setup)")
		remote := fs.String("remote", "", "<drive-id>:<root-path> (first-time setup)")
		yes := fs.Bool("yes", false, "Confirm drive ID change")
		fs.Parse(os.Args[2:])
		commands.CmdInit(*tenant, *client, *remote, *yes)
	case "login":
		commands.CmdLogin()
	case "logout":
		commands.CmdLogout()
	case "doctor":
		commands.CmdDoctor(version)
	case "lfs-agent":
		if err := agent.Run(os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "agent error:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}
