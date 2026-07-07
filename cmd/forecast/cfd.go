package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"forecasting/internal/cfd"
	"forecasting/internal/linear"
	"forecasting/internal/sqlite"
	"forecasting/internal/util"
)

func cmdCFD(args []string) error {
	cmd := flag.NewFlagSet("cfd", flag.ExitOnError)
	dbFile := cmd.String("db", "", "path to SQLite database")
	startStr := cmd.String("start", "", "start date, inclusive (YYYY-MM-DD; default: today minus 3 months)")
	endStr := cmd.String("end", "", "end date, inclusive (YYYY-MM-DD; default: today)")
	format := cmd.String("format", "html", "output format: html, json")
	outPath := cmd.String("out", "", "write output to this file instead of stdout")
	var teams linear.KeyList
	cmd.Var(&teams, "teams", "comma-separated team keys to filter by (e.g. ENG,DATA); default: all teams")
	configFile := cmd.String("config", "", "path to a YAML config file supplying flag values (CLI flags override)")
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	if *dbFile == "" {
		return fmt.Errorf("-db is required")
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)

	windowEnd := today
	if *endStr != "" {
		t, err := util.ParseDate(*endStr)
		if err != nil {
			return fmt.Errorf("invalid -end %q: %w", *endStr, err)
		}
		windowEnd = t
	}

	windowStart := today.AddDate(0, -3, 0)
	if *startStr != "" {
		t, err := util.ParseDate(*startStr)
		if err != nil {
			return fmt.Errorf("invalid -start %q: %w", *startStr, err)
		}
		windowStart = t
	}

	if !windowStart.Before(windowEnd) {
		return fmt.Errorf("-start must be before -end")
	}

	opts := cfd.Options{Teams: teams, Start: windowStart, End: windowEnd}

	store, err := sqlite.Open(*dbFile)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	raw, err := store.CFDIssues(context.Background(), opts.Teams)
	if err != nil {
		return fmt.Errorf("query issues: %w", err)
	}

	var normalized []cfd.NormalizedIssue
	skipped := 0
	for _, r := range raw {
		ni, ok := cfd.Normalize(r)
		if !ok {
			skipped++
			continue
		}
		normalized = append(normalized, ni)
	}

	rows := cfd.BuildGrid(normalized, opts.Start, opts.End)

	if err := cfd.AssertInvariants(rows); err != nil {
		return fmt.Errorf("CFD invariant violated: %w", err)
	}

	health := cfd.ComputeHealth(rows, normalized, opts.Start, opts.End)
	health.TotalIssues = len(raw)
	health.SkippedIssues = skipped

	out := os.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer f.Close()
		out = f
	}

	switch *format {
	case "html":
		return cfd.RenderHTML(out, rows, health, len(raw), skipped, opts.Start, opts.End)
	case "json":
		return cfd.RenderJSON(out, rows, health)
	default:
		return fmt.Errorf("unknown -format %q (use html or json)", *format)
	}
}
