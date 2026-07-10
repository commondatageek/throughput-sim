package util

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStringify(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"int", 4, "4"},
		{"float", 3.14, "3.14"},
		{"bool_true", true, "true"},
		{"bool_false", false, "false"},
		{"string", "hello", "hello"},
		{"slice", []any{5, 25, 50}, "5,25,50"},
		{"slice_mixed", []any{"alice", "bob"}, "alice,bob"},
		{"empty_slice", []any{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stringify(tc.in); got != tc.want {
				t.Errorf("stringify(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// commaList is a minimal flag.Value for testing list-join behavior.
type commaList []string

func (l *commaList) Set(s string) error {
	for _, tok := range strings.Split(s, ",") {
		*l = append(*l, tok)
	}
	return nil
}

func (l *commaList) String() string { return strings.Join(*l, ",") }

func newTestFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("db", "", "")
	fs.Int("engineers", 3, "")
	fs.Bool("whole-team", false, "")
	var pcts commaList
	fs.Var(&pcts, "percentile", "")
	var team commaList
	fs.Var(&team, "team", "")
	return fs
}

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(f, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

func isFlagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func TestApplyConfig(t *testing.T) {
	t.Run("populates_unset_flags", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		if err := applyConfig(fs, map[string]any{"engineers": 7, "whole-team": false}); err != nil {
			t.Fatal(err)
		}
		got := fs.Lookup("engineers").Value.String()
		if got != "7" {
			t.Errorf("engineers = %q, want %q", got, "7")
		}
	})

	t.Run("cli_flag_not_overridden_by_config", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{"-engineers", "2"})
		if err := applyConfig(fs, map[string]any{"engineers": 9}); err != nil {
			t.Fatal(err)
		}
		got := fs.Lookup("engineers").Value.String()
		if got != "2" {
			t.Errorf("engineers = %q, want %q (CLI should win)", got, "2")
		}
	})

	t.Run("config_set_flag_reports_as_set", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		if err := applyConfig(fs, map[string]any{"engineers": 5}); err != nil {
			t.Fatal(err)
		}
		if !isFlagSet(fs, "engineers") {
			t.Error("isFlagSet(engineers) should be true after applyConfig")
		}
	})

	t.Run("unknown_key_errors", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		if err := applyConfig(fs, map[string]any{"enginers": 4}); err == nil {
			t.Error("expected error for unknown key, got nil")
		}
	})

	t.Run("joins_multiple_key_errors", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		err := applyConfig(fs, map[string]any{"enginers": 4, "wole-team": true})
		if err == nil {
			t.Fatal("expected error for unknown keys, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, "enginers") {
			t.Errorf("error %q missing key %q", msg, "enginers")
		}
		if !strings.Contains(msg, "wole-team") {
			t.Errorf("error %q missing key %q", msg, "wole-team")
		}
	})

	t.Run("yaml_list_parses_into_commaList", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		if err := applyConfig(fs, map[string]any{"percentile": []any{5, 25, 50}}); err != nil {
			t.Fatal(err)
		}
		got := fs.Lookup("percentile").Value.String()
		if got != "5,25,50" {
			t.Errorf("percentile = %q, want %q", got, "5,25,50")
		}
	})

	t.Run("config_key_skipped", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.String("config", "", "")
		fs.Parse([]string{})
		if err := applyConfig(fs, map[string]any{"config": "some-other.yaml", "engineers": 6}); err != nil {
			t.Fatal(err)
		}
		got := fs.Lookup("engineers").Value.String()
		if got != "6" {
			t.Errorf("engineers = %q, want %q", got, "6")
		}
	})

	t.Run("missing_file_errors", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		if err := ApplyConfig(fs, "/no/such/file.yaml"); err == nil {
			t.Error("expected error for missing file, got nil")
		}
	})

	t.Run("empty_path_noop", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		if err := ApplyConfig(fs, ""); err != nil {
			t.Errorf("expected no error for empty path, got %v", err)
		}
	})

	t.Run("comma_string_parses_into_commaList", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		path := writeYAML(t, "percentile: \"5,25,50\"\n")
		if err := ApplyConfig(fs, path); err != nil {
			t.Fatal(err)
		}
		got := fs.Lookup("percentile").Value.String()
		if got != "5,25,50" {
			t.Errorf("percentile = %q, want %q", got, "5,25,50")
		}
	})
}
