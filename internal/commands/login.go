package commands

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/kint-pro/kint-data-cli/internal/auth"
	"github.com/kint-pro/kint-data-cli/internal/config"
)

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func CmdLogin() {
	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	client, err := auth.NewClient(cfg.TenantID, cfg.ClientID)
	if err != nil {
		fail(err)
	}
	ctx := context.Background()
	if _, err := auth.TokenSilent(ctx, client); err == nil {
		fmt.Printf("already signed in as %s\n", auth.AccountName(ctx, client))
		return
	}
	flowCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	code, err := client.AcquireTokenByDeviceCode(flowCtx, auth.Scopes)
	if err != nil {
		fail(err)
	}
	fmt.Println(code.Result.Message)
	result, err := code.AuthenticationResult(flowCtx)
	if err != nil {
		fail(err)
	}
	fmt.Printf("signed in as %s\n", result.Account.PreferredUsername)
}

func CmdLogout() {
	if err := auth.RemoveCache(); err != nil {
		fail(err)
	}
	fmt.Println("local session removed")
	fmt.Println("note: server-side sessions stay valid - offboarding is done by removing the member from the kint M365 group")
}
