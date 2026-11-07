// Command worker is tierbound's propose-only pgmq consumer.
// See docs/IMPLEMENTATION_PLAN.md step 1.4.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/MohamedBenTaher/tierbound/worker/internal/agentconfig"
)

func main() {
	printHash := flag.Bool("print-config-hash", false, "print the agent config_hash and exit")
	configPath := flag.String("config", "", "path to the agent config TOML (default: $AGENT_CONFIG_PATH, then config/triage-drafter.toml)")
	flag.Parse()

	if *printHash {
		cfg, err := agentconfig.Load(resolveConfigPath(*configPath))
		if err != nil {
			fmt.Fprintln(os.Stderr, "worker:", err)
			os.Exit(1)
		}
		h, err := cfg.Hash()
		if err != nil {
			fmt.Fprintln(os.Stderr, "worker:", err)
			os.Exit(1)
		}
		fmt.Println(h)
		return
	}

	fmt.Fprintln(os.Stderr, "tierbound worker: not implemented yet (step 1.4)")
}

// resolveConfigPath prefers the flag, then the env, then the shipped file.
// The two candidates cover running from worker/ (go run) and from the repo
// root (scripts, pre-commit).
func resolveConfigPath(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("AGENT_CONFIG_PATH"); env != "" {
		return env
	}
	for _, candidate := range []string{"config/triage-drafter.toml", "worker/config/triage-drafter.toml"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "config/triage-drafter.toml"
}
