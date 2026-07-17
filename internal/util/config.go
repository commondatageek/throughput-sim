package util

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"forecasting/internal/logx"

	"gopkg.in/yaml.v3"
)

// ApplyConfig reads a YAML file of flag values and applies each to fs for any
// flag not already set on the command line, giving precedence:
// CLI flag > config file > flag default. Call it immediately after
// fs.Parse(args) and before any flag-presence-dependent logic: config values are
// applied via fs.Set, so a Visit-based "is this flag set?" check reports them as
// set — intended, so a value in the config drives the same behavior as passing
// it on the command line.
//
// Regardless of whether path is set, ApplyConfig logs the effective value and
// source (default/config file/CLI flag) of every flag in fs — unexpected
// defaults are as often the cause of a confusing run as a bad config value.
func ApplyConfig(fs *flag.FlagSet, path string) error {
	cliSet := snapshotSetFlags(fs)

	var configSet map[string]bool
	var err error
	if path != "" {
		var raw map[string]any
		if raw, err = loadConfig(path); err == nil {
			configSet, err = applyConfig(fs, raw, cliSet)
		}
	}

	logEffectiveFlags(fs, cliSet, configSet)
	return err
}

// snapshotSetFlags reports which flags in fs were explicitly set (so far) —
// on the command line if called right after fs.Parse.
func snapshotSetFlags(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

// loadConfig reads and parses a YAML config file into a raw key/value map.
func loadConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	return raw, nil
}

// applyConfig applies each config value to fs for any flag not already set on
// the command line (per cliSet, snapshotted by the caller before any config
// value is applied, so a config value can never override an explicit
// command-line flag). Keys are processed in sorted order so the joined error
// is deterministic, and every failing key is reported (via errors.Join)
// rather than only the first — so one run surfaces all bad keys. Returns the
// set of flag names successfully set from config, for source-attribution in
// the effective-flags report.
func applyConfig(fs *flag.FlagSet, raw map[string]any, cliSet map[string]bool) (map[string]bool, error) {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	configSet := map[string]bool{}
	var errs []error
	for _, k := range keys {
		if k == "config" || cliSet[k] {
			continue // reserved key / CLI wins
		}
		if err := fs.Set(k, stringify(raw[k])); err != nil {
			errs = append(errs, fmt.Errorf("config key %q: %w", k, err))
			continue
		}
		configSet[k] = true
	}
	return configSet, errors.Join(errs...)
}

// logEffectiveFlags logs, at Info level, an aligned table of every flag in fs
// with its current value and where that value came from: a CLI flag beats a
// config file value, which beats the flag's built-in default. configSet may
// be nil (no config file applied, or path == "" in ApplyConfig) — reads on a
// nil map are zero-valued, so every flag simply falls through to "default".
//
// Column alignment is computed via text/tabwriter (the same idiom
// cmd/forecast/backtest.go uses for its table output) into a buffer, then
// split into lines so each row still goes through logx.Infof like any other
// log line.
func logEffectiveFlags(fs *flag.FlagSet, cliSet, configSet map[string]bool) {
	logx.Infof("effective flag values for this run:")

	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FLAG\tVALUE\tSOURCE")
	fs.VisitAll(func(f *flag.Flag) {
		source := "default"
		switch {
		case cliSet[f.Name]:
			source = "CLI flag"
		case configSet[f.Name]:
			source = "config file"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", f.Name, f.Value.String(), source)
	})
	tw.Flush()

	for _, line := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		logx.Infof("%s", line)
	}
}

// stringify renders a YAML scalar or sequence into the string form the flags'
// Set methods expect. Sequences (e.g. percentile: [5, 25, 50]) join with commas
// to match the comma-separated syntax that flag list types already accept;
// scalars use fmt.Sprint (engineers: 4 -> "4", whole-team: true -> "true").
// Writing the string form (percentile: "5,25,50") works identically.
func stringify(v any) string {
	if seq, ok := v.([]any); ok {
		parts := make([]string, len(seq))
		for i, e := range seq {
			parts[i] = fmt.Sprint(e)
		}
		return strings.Join(parts, ",")
	}
	return fmt.Sprint(v)
}
