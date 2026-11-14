// Command evalscore runs the golden set against an agent config (plan steps
// 3.1-3.2).
//
//	go run ./cmd/evalscore -dry-run ../evals/golden   # load and validate only
//	go run ./cmd/evalscore -record  ../evals/golden   # draft with the model, write the recording, score
//	go run ./cmd/evalscore -live    ../evals/golden   # draft with the model, score, write nothing
//	go run ./cmd/evalscore          ../evals/golden   # replay the recording for this config_hash (CI)
//
// Replay is deterministic and needs no API key: the recording for the
// config's exact config_hash must exist and score as passing. Any config
// change produces a new hash, so it needs a new recording (made with -record
// by whoever changes the config) before the gate goes green.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MohamedBenTaher/tierbound/worker/internal/agentconfig"
	"github.com/MohamedBenTaher/tierbound/worker/internal/evals"
	"github.com/MohamedBenTaher/tierbound/worker/internal/llm"
	"github.com/MohamedBenTaher/tierbound/worker/internal/propose"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "evalscore:", err)
		os.Exit(1)
	}
}

func run() error {
	dryRun := flag.Bool("dry-run", false, "load and validate fixtures only")
	record := flag.Bool("record", false, "draft with the model and write the recording for this config_hash")
	live := flag.Bool("live", false, "draft with the model and score without writing anything")
	configPath := flag.String("config", "config/triage-drafter.toml", "agent config TOML")
	rubricPath := flag.String("rubric", "", "rubric JSON (default: <golden>/../rubric.json)")
	resultsDir := flag.String("results", "", "recordings dir (default: <golden>/../results)")
	flag.Parse()
	if flag.NArg() != 1 {
		return errors.New("usage: evalscore [-dry-run|-record|-live] <golden dir>")
	}
	golden := flag.Arg(0)
	if *rubricPath == "" {
		*rubricPath = filepath.Join(golden, "..", "rubric.json")
	}
	if *resultsDir == "" {
		*resultsDir = filepath.Join(golden, "..", "results")
	}

	set, err := evals.Load(golden, *rubricPath)
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Printf("loaded %d fixtures, threshold %.2f, %d global must-not rules\n",
			len(set.Fixtures), set.Rubric.Threshold, len(set.Rubric.MustNotSay))
		return nil
	}

	cfg, err := agentconfig.Load(*configPath)
	if err != nil {
		return err
	}
	hash, err := cfg.Hash()
	if err != nil {
		return err
	}
	fmt.Printf("config %s  config_hash %s\n", cfg.Name, hash)

	var rec evals.Recording
	switch {
	case *record || *live:
		rec, err = draftAll(cfg, hash, set)
		if err != nil {
			return err
		}
		if *record {
			if err := writeRecording(evals.RecordingPath(*resultsDir, hash), rec); err != nil {
				return err
			}
			fmt.Println("wrote", evals.RecordingPath(*resultsDir, hash))
		}
	default:
		p := evals.RecordingPath(*resultsDir, hash)
		if err := readRecording(p, &rec); err != nil {
			return fmt.Errorf("no usable recording for config_hash %s (%w); run `go run ./cmd/evalscore -record %s` with GROQ_API_KEY set and commit it", hash, err, golden)
		}
		if rec.ConfigHash != hash {
			return fmt.Errorf("%s records config_hash %s, want %s", p, rec.ConfigHash, hash)
		}
	}

	rep := set.Score(rec.Outputs)
	printReport(rep)
	if !rep.Pass {
		return fmt.Errorf("eval gate FAILED for config_hash %s", hash)
	}
	fmt.Printf("eval gate passed for config_hash %s\n", hash)
	return nil
}

func draftAll(cfg agentconfig.Config, hash string, set *evals.Set) (evals.Recording, error) {
	key := os.Getenv("GROQ_API_KEY")
	if key == "" {
		return evals.Recording{}, errors.New("GROQ_API_KEY is not set")
	}
	client := llm.New(key, cfg.Model, os.Getenv("GROQ_API_URL"))
	rec := evals.Recording{
		ConfigHash: hash,
		Model:      cfg.Model,
		RecordedAt: time.Now().UTC().Format(time.RFC3339),
		Outputs:    map[string]string{},
	}
	for _, f := range set.Fixtures {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		out, err := client.DraftComment(ctx, cfg.Prompt, propose.FormIssueMessage(f.Issue.GitHub()))
		cancel()
		if err != nil {
			return evals.Recording{}, fmt.Errorf("fixture %s: %w", f.ID, err)
		}
		rec.Outputs[f.ID] = out
	}
	return rec, nil
}

func printReport(rep evals.Report) {
	for _, r := range rep.Results {
		fmt.Printf("  %-36s score %.2f  %s", r.ID, r.Score, checksLine(r))
		for _, v := range r.Violations {
			fmt.Printf("\n      MUST-NOT: %s", v)
		}
		fmt.Println()
	}
	for _, id := range rep.Missing {
		fmt.Printf("  %-28s MISSING from recording\n", id)
	}
	fmt.Printf("mean %.3f (threshold %.2f), must-not violations %d, missing %d\n",
		rep.Mean, rep.Threshold, rep.Violations, len(rep.Missing))
}

// checksLine renders the scorer's checks: the issue scorer's named map, or
// the comment scorer's three fixed checks.
func checksLine(r evals.FixtureResult) string {
	if r.Checks == nil {
		return fmt.Sprintf("structure=%t area=%t label=%t", r.Structure, r.Area, r.Label)
	}
	names := make([]string, 0, len(r.Checks))
	for name := range r.Checks {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s=%t", name, r.Checks[name])
	}
	return strings.Join(parts, " ")
}

func writeRecording(path string, rec evals.Recording) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func readRecording(path string, rec *evals.Recording) error {
	b, err := os.ReadFile(path) //nolint:gosec // path is derived from the operator's results dir and a hex hash
	if err != nil {
		return err
	}
	return json.Unmarshal(b, rec)
}
