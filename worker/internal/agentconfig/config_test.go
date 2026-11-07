package agentconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// configToml builds a valid config body with tier and tool_grant set
// exactly once, so overrides never trip TOML duplicate-key rejection.
func configToml(tier, toolGrant string) string {
	return `
name = "triage-drafter"
tier = "` + tier + `"
model = "openai/gpt-oss-20b"
tool_grant = ` + toolGrant + `
`
}

func writeConfig(t *testing.T, tomlBody, prompt string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "prompt_path = \"prompt.md\"\n" + tomlBody
	if err := os.WriteFile(filepath.Join(dir, "agent.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "agent.toml")
}

func hashOf(t *testing.T, path string) string {
	t.Helper()
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := cfg.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 64 {
		t.Fatalf("config_hash is not a sha256 hex digest: %q", h)
	}
	return h
}

func TestShippedConfigHashesStable(t *testing.T) {
	path := filepath.Join("..", "..", "config", "triage-drafter.toml")
	if a, b := hashOf(t, path), hashOf(t, path); a != b {
		t.Fatalf("config_hash is not stable across loads: %s != %s", a, b)
	}
}

func TestHashBindsPromptContent(t *testing.T) {
	body := configToml("draft", `["github.read_issues"]`)
	a := hashOf(t, writeConfig(t, body, "draft a summary"))
	b := hashOf(t, writeConfig(t, body, "draft a summary, then a label"))
	if a == b {
		t.Fatal("changing prompt content did not change config_hash")
	}
}

func TestHashIgnoresToolGrantOrder(t *testing.T) {
	ordered := configToml("draft", `["github.read_issues", "github.read_labels"]`)
	shuffled := configToml("draft", `["github.read_labels", "github.read_issues"]`)
	a := hashOf(t, writeConfig(t, ordered, "p"))
	b := hashOf(t, writeConfig(t, shuffled, "p"))
	if a != b {
		t.Fatalf("tool_grant order changed config_hash: %s != %s", a, b)
	}
}

func TestLoadRejectsUnknownTier(t *testing.T) {
	path := writeConfig(t, configToml("commit", `["github.read_issues"]`), "p")
	if _, err := Load(path); err == nil {
		t.Fatal("tier commit was accepted; only read and draft exist")
	}
}

func TestLoadRejectsMissingPrompt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	body := "name = \"x\"\ntier = \"read\"\nmodel = \"m\"\ntool_grant = [\"g\"]\nprompt_path = \"missing.md\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("missing prompt file was accepted")
	}
}
