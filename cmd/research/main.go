// Command research exports private frozen evidence and evaluates reviewed replays.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/headcount/internal/research"
	"git.subcult.tv/subculture-collective/headcount/internal/score"
	"git.subcult.tv/subculture-collective/headcount/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: research export|replay|review|case|sample|control-cases|evaluate|calibrate|probability-evaluate|persistence|record-context [flags]")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	in := f.String("in", "", "input packet, case list, or calibration artifact")
	out := f.String("out", "", "new private output file (never overwritten)")
	controls := f.String("controls", "docs/research-controls.json", "owner-nominated control roster")
	pool := f.String("pool", "", "directory of exported snapshot packets")
	stream := f.String("stream", "", "stream ID to export")
	at := f.String("at", "", "exact snapshot time (RFC3339Nano); default latest")
	omit := f.String("omit", "", "comma-separated components or families for ablation")
	target := f.String("target", "audience", "audience, chat, or follows")
	split := f.String("split", "test", "evaluation partition")
	casesPath := f.String("cases", "", "reviewed case list for probability/persistence evaluation")
	threshold := f.Float64("threshold", 0.3, "uncalibrated mechanism evidence threshold")
	coverage := f.Float64("coverage", 0.1, "minimum observation coverage")
	n := f.Int("n", 50, "representative sample size")
	seed := f.Uint64("seed", 17, "reproducible sampling seed")
	from := f.String("from", "", "context start (RFC3339)")
	until := f.String("until", "", "context end (RFC3339)")
	kind := f.String("kind", "", "embed, promotion, drops, ads, or collaboration")
	source := f.String("source", "", "context source reference")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("-out is required")
	}
	omissions := []string{}
	for _, x := range strings.Split(*omit, ",") {
		if x = strings.TrimSpace(x); x != "" {
			omissions = append(omissions, x)
		}
	}
	var result any
	switch args[0] {
	case "record-context":
		id, err := strconv.ParseInt(*stream, 10, 64)
		if err != nil || id <= 0 {
			return errors.New("valid -stream required")
		}
		start, err := time.Parse(time.RFC3339, *from)
		if err != nil {
			return err
		}
		end, err := time.Parse(time.RFC3339, *until)
		if err != nil {
			return err
		}
		if os.Getenv("DATABASE_URL") == "" {
			return errors.New("DATABASE_URL required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		db, err := store.Open(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			return err
		}
		defer db.Close()
		// Reserve the receipt first so an existing file cannot cause an unreported mutation.
		receipt := map[string]any{"stream_id": id, "from": start, "until": end, "kind": *kind, "source": *source, "status": "pending"}
		if err := research.WriteNew(*out, receipt); err != nil {
			return err
		}
		if err := db.RecordDistributionContext(ctx, id, start, end, *kind, *source); err != nil {
			return fmt.Errorf("context not recorded; pending receipt at %s: %w", *out, err)
		}
		receipt["status"] = "recorded"
		if err := research.WriteNew(*out+".confirmed.json", receipt); err != nil {
			return fmt.Errorf("context WAS recorded; confirmation receipt failed: %w", err)
		}
		return nil
	case "export":
		id, err := strconv.ParseInt(*stream, 10, 64)
		if err != nil || id <= 0 {
			return errors.New("valid -stream required")
		}
		url := os.Getenv("DATABASE_URL")
		if url == "" {
			return errors.New("DATABASE_URL required for export")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		db, err := store.Open(ctx, url)
		if err != nil {
			return fmt.Errorf("open evidence database: %w", err)
		}
		defer db.Close()
		var when *time.Time
		if *at != "" {
			v, err := time.Parse(time.RFC3339Nano, *at)
			if err != nil {
				return err
			}
			when = &v
		}
		var input, output []byte
		if err := db.Pool.QueryRow(ctx, `SELECT input,output FROM score_snapshots WHERE stream_id=$1 AND ($2::timestamptz IS NULL OR observed_at=$2) ORDER BY observed_at DESC,version DESC LIMIT 1`, id, when).Scan(&input, &output); err != nil {
			return err
		}
		var p research.Packet
		if err := json.Unmarshal(input, &p.Input); err != nil {
			return err
		}
		if err := json.Unmarshal(output, &p.Output); err != nil {
			return err
		}
		p.Digest, err = research.Digest(p.Input)
		if err != nil {
			return err
		}
		if err = p.Verify(); err != nil {
			return err
		}
		result = p
	case "replay", "review", "case":
		p, err := research.ReadPacket(*in)
		if err != nil {
			return err
		}
		switch args[0] {
		case "replay":
			result = struct {
				Source string       `json:"source_input_sha256"`
				Omit   []string     `json:"omit"`
				Result score.Result `json:"result"`
			}{p.Digest, omissions, score.Evaluate(p.Input, omissions)}
		case "review":
			result = research.Blind(p)
		case "case":
			result = []research.Case{{ID: p.Digest, Input: p.Input, Digest: p.Digest, Split: "development", Stratum: "challenge", Challenge: true, InclusionProbability: 1, Reviews: []research.Review{}}}
		}
	case "control-cases":
		ps, err := readPool(*pool)
		if err != nil {
			return err
		}
		var roster research.ControlRoster
		if err := readJSON(*controls, &roster); err != nil {
			return err
		}
		result, err = research.ControlCases(ps, roster)
		if err != nil {
			return err
		}
	case "sample":
		ps, err := readPool(*pool)
		if err != nil {
			return err
		}
		result, err = research.Sample(ps, *n, *seed)
		if err != nil {
			return err
		}
	case "evaluate", "calibrate":
		var cs []research.Case
		if err := readJSON(*in, &cs); err != nil {
			return err
		}
		var err error
		if args[0] == "evaluate" {
			result, err = research.Evaluate(cs, *split, *target, *threshold, *coverage, omissions)
		} else {
			result, err = research.FitCalibration(cs, *target, *coverage)
		}
		if err != nil {
			return err
		}
	case "probability-evaluate":
		var model research.Calibration
		var cs []research.Case
		if err := readJSON(*in, &model); err != nil {
			return err
		}
		if err := readJSON(*casesPath, &cs); err != nil {
			return err
		}
		var err error
		result, err = research.EvaluateCalibration(model, cs)
		if err != nil {
			return err
		}
	case "persistence":
		ps, err := readPool(*pool)
		if err != nil {
			return err
		}
		var cs []research.Case
		if err := readJSON(*casesPath, &cs); err != nil {
			return err
		}
		result, err = research.Persistence(ps, cs, *target, *threshold, *coverage)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	return research.WriteNew(*out, result)
}
func readPool(dir string) ([]research.Packet, error) {
	if dir == "" {
		return nil, errors.New("-pool required")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []research.Packet
	for _, path := range paths {
		p, err := research.ReadPacket(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, p)
	}
	return out, nil
}
