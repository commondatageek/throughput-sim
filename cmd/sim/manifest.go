package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"forecasting/internal/linear"
)

// Manifest is a single JSON document recording everything that fed a
// simulation run, so two disagreeing runs can be diffed: the binary's git
// SHA, a fingerprint of the DB file, every flag (set vs. defaulted), the
// resolved sampling mode/seed/window, the built sample pool, and a full dump
// of the issues that fed it.
type Manifest struct {
	SchemaVersion int           `json:"schema_version"`
	GeneratedAt   string        `json:"generated_at"`
	Binary        BinaryInfo    `json:"binary"`
	Invocation    Invocation    `json:"invocation"`
	Flags         []FlagRecord  `json:"flags"`
	Resolved      Resolved      `json:"resolved"`
	Data          DataSection   `json:"data"`
	Pool          PoolSection   `json:"pool"`
	Issues        IssuesSection `json:"issues"`
}

type BinaryInfo struct {
	GitSHA          string `json:"git_sha"`
	GitTime         string `json:"git_time"`
	Dirty           bool   `json:"dirty"`
	GoVersion       string `json:"go_version"`
	Module          string `json:"module"`
	VersionOverride string `json:"version_override,omitempty"`
}

type Invocation struct {
	Subcommand string   `json:"subcommand"`
	Args       []string `json:"args"`
	WorkingDir string   `json:"working_dir"`
}

type FlagRecord struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Default string `json:"default"`
	Set     bool   `json:"set"`
}

type Resolved struct {
	Mode        string         `json:"mode"`
	ModeLabel   string         `json:"mode_label"`
	Engineers   int            `json:"engineers"`
	Team        []string       `json:"team"`
	Include     []string       `json:"include"`
	WholeTeam   bool           `json:"whole_team"`
	Seed        int64          `json:"seed"`
	SampleStart string         `json:"sample_start"`
	SampleEnd   string         `json:"sample_end"`
	TotalDays   int            `json:"total_days"`
	Extra       map[string]any `json:"extra,omitempty"`
}

type DataSection struct {
	DB                DataFile   `json:"db"`
	ExclusionsPath    string     `json:"exclusions_path"`
	ExclusionsApplied Exclusions `json:"exclusions_applied"`
}

type DataFile struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime string `json:"modtime"`
	SHA256  string `json:"sha256"`
	Error   string `json:"error,omitempty"`
}

type PoolSection struct {
	EngineerCount          int            `json:"engineer_count"`
	PerEngineerCompletions map[string]int `json:"per_engineer_completions"`
	PerEngineerSampleDays  map[string]int `json:"per_engineer_sample_days"`
	TotalSampleDays        int            `json:"total_sample_days"`
	TotalCompletions       int            `json:"total_completions"`
	CombinedSampleCount    int            `json:"combined_sample_count"`
}

type IssuesSection struct {
	QueriedCount int           `json:"queried_count"`
	SkippedCount int           `json:"skipped_count"`
	Records      []IssueRecord `json:"records"`
}

// IssueRecord mirrors the subset of linear.Issue that CompletedBetween
// populates (TeamKey, ProjectID, milestone, ArchivedAt, etc. are not selected
// by that query and are intentionally omitted).
type IssueRecord struct {
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Assignee    string `json:"assignee"`
	TeamName    string `json:"team_name"`
	ProjectName string `json:"project_name"`
	StateType   string `json:"state_type"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// version is an optional build-time override: go build -ldflags "-X main.version=...".
// The primary SHA source is debug.ReadBuildInfo (auto VCS stamping).
var version string

// buildInfo reads VCS stamping info embedded by `go build` from the git tree
// (Go's automatic vcs.* build settings). It degrades gracefully (empty SHA,
// dirty=false) when build info is absent, e.g. under `go run` or for a binary
// built outside a git checkout.
func buildInfo() BinaryInfo {
	bi := BinaryInfo{VersionOverride: version}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return bi
	}
	bi.GoVersion = info.GoVersion
	bi.Module = info.Main.Path
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			bi.GitSHA = s.Value
		case "vcs.time":
			bi.GitTime = s.Value
		case "vcs.modified":
			bi.Dirty = s.Value == "true"
		}
	}
	return bi
}

// dbFingerprint identifies the exact DB file a run used: absolute path, size,
// modtime, and a streamed sha256. Hash/stat errors are recorded as a
// non-fatal Error field rather than aborting the run.
func dbFingerprint(path string) DataFile {
	d := DataFile{Path: path}
	if abs, err := filepath.Abs(path); err == nil {
		d.Path = abs
	}
	info, err := os.Stat(d.Path)
	if err != nil {
		d.Error = err.Error()
		return d
	}
	d.Size = info.Size()
	d.ModTime = info.ModTime().UTC().Format(time.RFC3339)
	f, err := os.Open(d.Path)
	if err != nil {
		d.Error = err.Error()
		return d
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		d.Error = err.Error()
		return d
	}
	d.SHA256 = hex.EncodeToString(h.Sum(nil))
	return d
}

func modeName(m samplingMode) string {
	switch m {
	case modeNamedTeam:
		return "named_team"
	case modeFullTeam:
		return "whole_team"
	default:
		return "anonymous"
	}
}

func issueTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// manifestInputs is the single shared parameter bag the three subcommands
// fill in to produce a Manifest.
type manifestInputs struct {
	Subcommand     string
	Cmd            *flag.FlagSet
	Mode           samplingMode
	Team           []string
	Include        []string
	Engineers      int
	WholeTeam      bool
	Seed           int64
	SampleStart    time.Time
	SampleEnd      time.Time
	DBPath         string
	ExclusionsPath string
	Exclusions     Exclusions
	Pool           *SamplePool
	Issues         []linear.Issue
	Skipped        int
	Extra          map[string]any
}

// newManifest assembles a Manifest from manifestInputs. It is pure (no file
// or DB access of its own) so it's unit-testable in isolation.
func newManifest(in manifestInputs) *Manifest {
	// Flags: walk the FlagSet generically — captures every flag incl. the
	// command-unique ones (-days, -items, -start-date, -percentile, -manifest).
	var flags []FlagRecord
	in.Cmd.VisitAll(func(f *flag.Flag) {
		flags = append(flags, FlagRecord{
			Name:    f.Name,
			Value:   f.Value.String(),
			Default: f.DefValue,
			Set:     isFlagSet(in.Cmd, f.Name),
		})
	})

	// Pool summary: direct iteration works for both per-engineer and
	// whole-team modes (the latter has the single "__whole_team__" key).
	perComp := make(map[string]int, len(in.Pool.PerEngineer))
	perDays := make(map[string]int, len(in.Pool.PerEngineer))
	totalComp, totalDays := 0, 0
	for name, samples := range in.Pool.PerEngineer {
		s := sum(samples)
		perComp[name] = s
		perDays[name] = len(samples)
		totalComp += s
		totalDays += len(samples)
	}

	records := make([]IssueRecord, 0, len(in.Issues))
	for _, it := range in.Issues {
		records = append(records, IssueRecord{
			Identifier:  it.Identifier,
			Title:       it.Title,
			Assignee:    it.Assignee,
			TeamName:    it.TeamName,
			ProjectName: it.ProjectName,
			StateType:   it.StateType,
			StartedAt:   issueTime(it.StartedAt),
			CompletedAt: issueTime(it.CompletedAt),
			UpdatedAt:   issueTime(it.UpdatedAt),
		})
	}

	wd, _ := os.Getwd()

	return &Manifest{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Binary:        buildInfo(),
		Invocation:    Invocation{Subcommand: in.Subcommand, Args: os.Args, WorkingDir: wd},
		Flags:         flags,
		Resolved: Resolved{
			Mode:        modeName(in.Mode),
			ModeLabel:   modeLabel(in.Mode, in.Team, in.Engineers),
			Engineers:   in.Engineers,
			Team:        in.Team,
			Include:     in.Include,
			WholeTeam:   in.WholeTeam,
			Seed:        in.Seed,
			SampleStart: in.SampleStart.UTC().Format(time.RFC3339),
			SampleEnd:   in.SampleEnd.UTC().Format(time.RFC3339),
			TotalDays:   daysBetween(in.SampleStart, in.SampleEnd),
			Extra:       in.Extra,
		},
		Data: DataSection{
			DB:                dbFingerprint(in.DBPath),
			ExclusionsPath:    in.ExclusionsPath,
			ExclusionsApplied: in.Exclusions,
		},
		Pool: PoolSection{
			EngineerCount:          len(in.Pool.PerEngineer),
			PerEngineerCompletions: perComp,
			PerEngineerSampleDays:  perDays,
			TotalSampleDays:        totalDays,
			TotalCompletions:       totalComp,
			CombinedSampleCount:    len(in.Pool.GetCombinedSamples()),
		},
		Issues: IssuesSection{
			QueriedCount: len(in.Issues),
			SkippedCount: in.Skipped,
			Records:      records,
		},
	}
}

// writeManifest builds and writes a run manifest to path ("-" for stdout). It
// is a no-op when path is empty, so the flag is opt-in and free when unset.
func writeManifest(path string, in manifestInputs) error {
	if path == "" {
		return nil // disabled
	}
	var w io.Writer = os.Stdout
	if path != "-" {
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("creating manifest file: %w", err)
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(newManifest(in))
}
