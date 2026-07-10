package util

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ApplyConfig reads a YAML file of flag values and applies each to fs for any
// flag not already set on the command line, giving precedence:
// CLI flag > config file > flag default. Call it immediately after
// fs.Parse(args) and before any flag-presence-dependent logic: config values are
// applied via fs.Set, so a Visit-based "is this flag set?" check reports them as
// set — intended, so a value in the config drives the same behavior as passing
// it on the command line.
func ApplyConfig(fs *flag.FlagSet, path string) error {
	if path == "" {
		return nil
	}
	raw, err := loadConfig(path)
	if err != nil {
		return err
	}
	return applyConfig(fs, raw)
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
// the command line. Keys are processed in sorted order so the joined error is
// deterministic, and every failing key is reported (via errors.Join) rather than
// only the first — so one run surfaces all bad keys.
func applyConfig(fs *flag.FlagSet, raw map[string]any) error {
	// Snapshot flags set on the CLI *before* applying anything, so a config
	// value can never override an explicit command-line flag.
	cliSet := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { cliSet[f.Name] = true })

	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var errs []error
	for _, k := range keys {
		if k == "config" || cliSet[k] {
			continue // reserved key / CLI wins
		}
		if err := fs.Set(k, stringify(raw[k])); err != nil {
			errs = append(errs, fmt.Errorf("config key %q: %w", k, err))
		}
	}
	return errors.Join(errs...)
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
