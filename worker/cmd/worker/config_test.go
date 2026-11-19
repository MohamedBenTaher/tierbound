package main

import "testing"

func TestResolveConfigPathPrefersFlagThenEnv(t *testing.T) {
	t.Setenv("AGENT_CONFIG_PATH", "from-env.toml")
	if got := resolveConfigPath("from-flag.toml"); got != "from-flag.toml" {
		t.Fatalf("flag: got %q", got)
	}
	if got := resolveConfigPath(""); got != "from-env.toml" {
		t.Fatalf("env: got %q", got)
	}
}
