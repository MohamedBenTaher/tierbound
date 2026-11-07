// Command worker is tierbound's propose-only pgmq consumer.
// See docs/IMPLEMENTATION_PLAN.md step 1.4.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MohamedBenTaher/tierbound/worker/internal/agentconfig"
	"github.com/MohamedBenTaher/tierbound/worker/internal/githubro"
	"github.com/MohamedBenTaher/tierbound/worker/internal/llm"
	"github.com/MohamedBenTaher/tierbound/worker/internal/pgmq"
	"github.com/MohamedBenTaher/tierbound/worker/internal/propose"
)

func main() {
	printHash := flag.Bool("print-config-hash", false, "print the agent config_hash and exit")
	once := flag.Bool("once", false, "process at most one job and exit")
	configPath := flag.String("config", "", "path to the agent config TOML (default: $AGENT_CONFIG_PATH, then config/triage-drafter.toml)")
	flag.Parse()

	cfg, err := agentconfig.Load(resolveConfigPath(*configPath))
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}

	if *printHash {
		h, err := cfg.Hash()
		if err != nil {
			fmt.Fprintln(os.Stderr, "worker:", err)
			os.Exit(1)
		}
		fmt.Println(h)
		return
	}

	dbURL := os.Getenv("WORKER_DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "worker: WORKER_DATABASE_URL is not set")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker: connect:", err)
		os.Exit(1)
	}
	defer pool.Close()

	deps := propose.Deps{
		Pool:    pool,
		Queue:   pgmq.New(pool),
		Fetcher: githubro.New(os.Getenv("GITHUB_READ_TOKEN"), os.Getenv("GITHUB_API_URL")),
		Drafter: llm.New(os.Getenv("GROQ_API_KEY"), cfg.Model, os.Getenv("GROQ_API_URL")),
		Config:  cfg,
	}

	if *once {
		if _, err := propose.ProcessOne(ctx, deps); err != nil {
			fmt.Fprintln(os.Stderr, "worker: job failed:", err)
			os.Exit(1)
		}
		return
	}

	log.Printf("tierbound worker: polling jobs for agent %s", cfg.Name)
	for ctx.Err() == nil {
		processed, err := propose.ProcessOne(ctx, deps)
		if err != nil {
			log.Printf("job failed: %v", err)
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	log.Printf("tierbound worker: stopped")
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
