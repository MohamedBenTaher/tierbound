// Package agentconfig loads an agent's pinned TOML config and computes its
// config_hash. The hash is the config's identity: the eval gate and the
// approval flow both bind to it (SPEC section 5).
package agentconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/BurntSushi/toml"

	"github.com/MohamedBenTaher/tierbound/worker/internal/hashing"
)

// Tier is the agent's grant level. Read agents only fetch; draft agents
// propose actions that need human approval before they take effect.
type Tier string

// The two tiers that exist (SPEC section 2). Anything else fails to load.
const (
	TierRead  Tier = "read"
	TierDraft Tier = "draft"
)

// Config is one agent's pinned config. Prompt holds the loaded prompt
// content because the prompt, not its path, is part of the config identity.
type Config struct {
	Name       string   `toml:"name"`
	Tier       Tier     `toml:"tier"`
	Model      string   `toml:"model"`
	PromptPath string   `toml:"prompt_path"`
	ToolGrant  []string `toml:"tool_grant"`
	Prompt     string   `toml:"-"`
}

// Load reads a TOML config file and its prompt file. prompt_path resolves
// relative to the config file's directory. Everything external is validated
// here; callers can trust the Config afterwards.
func Load(path string) (Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return Config{}, fmt.Errorf("agentconfig: decode %s: %w", path, err)
	}
	if c.Name == "" {
		return Config{}, fmt.Errorf("agentconfig: %s: name is required", path)
	}
	if c.Model == "" {
		return Config{}, fmt.Errorf("agentconfig: %s: model is required", path)
	}
	if c.Tier != TierRead && c.Tier != TierDraft {
		return Config{}, fmt.Errorf("agentconfig: %s: tier %q: must be %q or %q", path, c.Tier, TierRead, TierDraft)
	}
	if len(c.ToolGrant) == 0 {
		return Config{}, fmt.Errorf("agentconfig: %s: tool_grant must not be empty", path)
	}
	if c.PromptPath == "" {
		return Config{}, fmt.Errorf("agentconfig: %s: prompt_path is required", path)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), c.PromptPath))
	if err != nil {
		return Config{}, fmt.Errorf("agentconfig: %s: read prompt: %w", path, err)
	}
	c.Prompt = string(b)
	return c, nil
}

// Hash returns the config_hash: the hex sha256 of canonical JSON over
// exactly {prompt, model, tool_grant, tier}. Keys are sorted by the
// canonical encoder; tool_grant is sorted here because grant order is
// presentation, not identity.
func (c Config) Hash() (string, error) {
	grant := slices.Clone(c.ToolGrant)
	slices.Sort(grant)
	return hashing.Canonical(map[string]any{
		"prompt":     c.Prompt,
		"model":      c.Model,
		"tool_grant": grant,
		"tier":       string(c.Tier),
	})
}
