// Package config holds provider-agnostic paths only. Anything specific to a
// particular LLM backend (OAuth client IDs, endpoint URLs, default models)
// lives inside the corresponding provider package, NOT here.
package config

import (
	"os"
	"path/filepath"
)

// Directory layout under $HOME:
//
//   ~/.mini/                  config root
//   ~/.mini/memory/           agent-managed memory (markdown files)
//   ~/.mini/jobs/             background job logs
//   ~/.mini/providers/<name>/ each provider's own state (creds, etc.)
//
// Older versions of mini stored everything under `~/.mini-cli/`. The first
// time we run after the rename, MigrateFromOldDir relocates the data so the
// user does not have to re-authenticate.
const (
	configDirName    = ".mini"
	oldConfigDirName = ".mini-cli"
)

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

// ConfigDir is the root for everything mini persists.
func ConfigDir() string { return filepath.Join(homeDir(), configDirName) }

// MemoryDir is the directory where agent memory markdown files live.
func MemoryDir() string { return filepath.Join(ConfigDir(), "memory") }

// ProviderStateDir returns the per-provider state directory
// (~/.mini/providers/<name>/). Provider packages are expected to write any
// credentials / cache / pending-auth state here, keeping the top-level
// config dir free of provider-specific files.
func ProviderStateDir(name string) string {
	return filepath.Join(ConfigDir(), "providers", name)
}

// MigrateFromOldDir moves ~/.mini-cli/ to ~/.mini/ if the new path does not
// yet exist. Idempotent. Errors are non-fatal — they just mean the user has
// to re-authenticate or move the files manually.
func MigrateFromOldDir() error {
	oldPath := filepath.Join(homeDir(), oldConfigDirName)
	newPath := ConfigDir()

	if _, err := os.Stat(newPath); err == nil {
		return nil
	}
	if _, err := os.Stat(oldPath); err != nil {
		return nil
	}
	return os.Rename(oldPath, newPath)
}
