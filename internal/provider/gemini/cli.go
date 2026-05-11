package gemini

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/arcaela/mini-cli/internal/provider"
	"github.com/arcaela/mini-cli/internal/provider/gemini/internal/oauth"
	"github.com/arcaela/mini-cli/internal/provider/gemini/internal/wire"
)

// DefaultModel is exported so the central CLI can ask each provider what to
// use when MINI_MODEL is unset. Provider-specific knowledge stays here.
const DefaultModel = "gemini-2.5-flash"

// init registers this provider with the central registry. The CLI dispatcher
// uses this to find both the Provider factory and the provider-specific
// subcommands (auth, auth-complete) without ever importing this package by
// name.
func init() {
	provider.Register("gemini", provider.Factory{
		New:          func(ctx context.Context) (provider.Provider, error) { return New(ctx) },
		DefaultModel: DefaultModel,
		Commands: map[string]provider.Command{
			"auth": {
				Description: "Print OAuth URL and persist PKCE state.",
				Run:         runAuth,
			},
			"auth-complete": {
				Description: "Exchange the pasted callback URL/code for tokens.",
				Run:         runAuthComplete,
			},
		},
	})
}

// runAuth — `mini provider gemini auth`
func runAuth(ctx context.Context, args []string) error {
	// Make sure legacy creds (~/.mini/creds.json) move to the new
	// provider-namespaced location before generating fresh pending state.
	_ = oauth.MigrateLegacyState()

	pkce, err := oauth.NewPkce()
	if err != nil {
		return err
	}
	if err := oauth.SavePending(pkce); err != nil {
		return err
	}
	authURL := oauth.BuildAuthURL(pkce)
	fmt.Println("\n=== STEP 1: open this URL in your browser and consent ===")
	fmt.Println()
	fmt.Println(authURL)
	fmt.Println()
	fmt.Println("After clicking Allow, your browser will show \"site can't be reached\".")
	fmt.Println("That's expected. Copy the URL from the address bar and run:")
	fmt.Println()
	fmt.Println("    mini provider gemini auth-complete \"<paste here>\"")
	fmt.Println()
	fmt.Println("PKCE state persisted to", oauth.PendingFile())
	return nil
}

// runAuthComplete — `mini provider gemini auth-complete <url-or-code>`
func runAuthComplete(ctx context.Context, args []string) error {
	pasted := strings.TrimSpace(strings.Join(args, " "))
	if pasted == "" {
		return fmt.Errorf("usage: mini provider gemini auth-complete \"<URL-or-code>\"")
	}
	pending, err := oauth.LoadPending()
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no pending auth; run `mini provider gemini auth` first")
		}
		return err
	}
	code, err := oauth.ExtractCode(pasted, pending.State)
	if err != nil {
		return err
	}
	creds, err := oauth.ExchangeCodeForTokens(ctx, code, pending.Verifier)
	if err != nil {
		return err
	}
	if ui, err := wire.New(creds.AccessToken).UserInfo(ctx); err == nil {
		creds.Email = ui.Email
	}
	if err := oauth.SaveCreds(creds); err != nil {
		return err
	}
	oauth.ClearPending()
	fmt.Println("✓ Credentials saved to", oauth.CredsFile())
	if creds.Email != "" {
		fmt.Println("  Account:", creds.Email)
	}
	return nil
}
