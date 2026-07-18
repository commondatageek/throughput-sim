package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/commondatageek/delivery-forecast/internal/linear"
	"github.com/commondatageek/delivery-forecast/simulate"
)

func TestBuildInfo_Degrades(t *testing.T) {
	// Under `go test`, debug.ReadBuildInfo() succeeds but vcs.* settings are
	// typically absent (no git-tree build), so GitSHA should come back empty
	// rather than panicking or erroring.
	bi := buildInfo()
	if bi.GoVersion == "" {
		t.Fatalf("buildInfo().GoVersion = %q, want non-empty", bi.GoVersion)
	}
}

func TestDBFingerprint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := dbFingerprint(path)
	if d.Error != "" {
		t.Fatalf("dbFingerprint error = %q, want none", d.Error)
	}
	if d.Size != 5 {
		t.Fatalf("Size = %d, want 5", d.Size)
	}
	// sha256("hello")
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if d.SHA256 != want {
		t.Fatalf("SHA256 = %q, want %q", d.SHA256, want)
	}
	if !filepath.IsAbs(d.Path) {
		t.Fatalf("Path = %q, want absolute", d.Path)
	}
}

func TestDBFingerprint_MissingFile(t *testing.T) {
	d := dbFingerprint(filepath.Join(t.TempDir(), "missing.db"))
	if d.Error == "" {
		t.Fatal("dbFingerprint on a missing file: want a non-empty Error, got none")
	}
}

func TestNewManifest_Assembly(t *testing.T) {
	cmd := flag.NewFlagSet("sim items", flag.ContinueOnError)
	dbFile := cmd.String("db", "", "")
	days := cmd.Int("days", 30, "")
	if err := cmd.Parse([]string{"-db", "test.db"}); err != nil {
		t.Fatal(err)
	}

	pool := simulate.NewSamplePool(map[string][]int{
		"alice": {1, 0, 2},
		"bob":   {0, 0},
	})
	issues := []linear.Issue{
		{Identifier: "ENG-1", Assignee: "alice", CompletedAt: day(2025, 1, 5)},
		{Identifier: "ENG-2", Assignee: "bob", CompletedAt: day(2025, 1, 6)},
	}

	m := newManifest(manifestInputs{
		Subcommand:  "sim items",
		Cmd:         cmd,
		Mode:        simulate.ModeAnonymous,
		Engineers:   3,
		Seed:        42,
		SampleStart: day(2025, 1, 1),
		SampleEnd:   day(2025, 2, 1),
		DBPath:      *dbFile,
		Pool:        pool,
		Issues:      issues,
		Skipped:     1,
		Extra:       map[string]any{"effective_percentiles": []int{5, 25, 50}},
	})

	if m.SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d, want 1", m.SchemaVersion)
	}
	if m.Invocation.Subcommand != "sim items" {
		t.Fatalf("Subcommand = %q, want %q", m.Invocation.Subcommand, "sim items")
	}

	var dbFlag, daysFlag *FlagRecord
	for i := range m.Flags {
		switch m.Flags[i].Name {
		case "db":
			dbFlag = &m.Flags[i]
		case "days":
			daysFlag = &m.Flags[i]
		}
	}
	if dbFlag == nil || !dbFlag.Set || dbFlag.Value != "test.db" {
		t.Fatalf("db flag = %+v, want Set=true Value=test.db", dbFlag)
	}
	if daysFlag == nil || daysFlag.Set || daysFlag.Value != "30" {
		t.Fatalf("days flag = %+v, want Set=false Value=30", daysFlag)
	}
	_ = days

	if m.Resolved.Seed != 42 {
		t.Fatalf("Resolved.Seed = %d, want 42", m.Resolved.Seed)
	}
	if m.Resolved.TotalDays != 31 {
		t.Fatalf("Resolved.TotalDays = %d, want 31", m.Resolved.TotalDays)
	}

	if m.Pool.EngineerCount != 2 {
		t.Fatalf("Pool.EngineerCount = %d, want 2", m.Pool.EngineerCount)
	}
	if got := m.Pool.PerEngineerCompletions["alice"]; got != 3 {
		t.Fatalf("PerEngineerCompletions[alice] = %d, want 3", got)
	}
	if got := m.Pool.TotalCompletions; got != 3 {
		t.Fatalf("Pool.TotalCompletions = %d, want 3", got)
	}
	if got := m.Pool.CombinedSampleCount; got != 5 {
		t.Fatalf("Pool.CombinedSampleCount = %d, want 5", got)
	}

	if m.Issues.QueriedCount != 2 || m.Issues.SkippedCount != 1 || len(m.Issues.Records) != 2 {
		t.Fatalf("Issues = %+v, want QueriedCount=2 SkippedCount=1 len(Records)=2", m.Issues)
	}
	if m.Issues.Records[0].Identifier != "ENG-1" || m.Issues.Records[0].Assignee != "alice" {
		t.Fatalf("Records[0] = %+v, want Identifier=ENG-1 Assignee=alice", m.Issues.Records[0])
	}
}
