package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"forecasting/internal/cfd"
	"forecasting/internal/logx"
	"forecasting/internal/sqlite"
	"forecasting/internal/util"
)

func cmdCFD(args []string) error {
	cmd := flag.NewFlagSet("cfd", flag.ExitOnError)
	dbFile := addDBFlag(cmd)
	startStr := cmd.String("start", "-3 months", `start date, inclusive (YYYY-MM-DD; or: yesterday, today, tomorrow, "-3 months")`)
	endStr := cmd.String("end", "today", `end date, inclusive (YYYY-MM-DD; or: now, yesterday, today, tomorrow, "-3 months")`)
	format := cmd.String("format", "html", "output format: html, json")
	outPath := cmd.String("out", "", "write output to this file instead of stdout")
	teams := addTeamsFlag(cmd, "comma-separated team keys to filter by (e.g. ENG,DATA); default: all teams")
	configFile := addConfigFlag(cmd)
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	if err := requireDB(dbFile); err != nil {
		return err
	}

	now := time.Now()

	windowEnd, err := util.ParseFlexibleDate(*endStr, now)
	if err != nil {
		return fmt.Errorf("invalid -end %q: %w", *endStr, err)
	}

	windowStart, err := util.ParseFlexibleStartDate(*startStr, now)
	if err != nil {
		return fmt.Errorf("invalid -start %q: %w", *startStr, err)
	}

	if !windowStart.Before(windowEnd) {
		return fmt.Errorf("-start must be before -end")
	}

	opts := cfd.Options{Teams: *teams, Start: windowStart, End: windowEnd}

	store, err := sqlite.OpenExisting(*dbFile)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	ctx := context.Background()

	if err := warnIfBlendingTeams(ctx, store, opts.Teams); err != nil {
		return err
	}

	raw, err := store.CFDIssues(ctx, opts.Teams)
	if err != nil {
		return fmt.Errorf("query issues: %w", err)
	}
	if len(raw) == 0 {
		logx.Warnf("no issues found in the database for the given team filter")
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
