package util

import (
	"bufio"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	w.Close()
	var sb strings.Builder
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		sb.WriteString(scanner.Text())
		sb.WriteString("\n")
	}
	return sb.String()
}

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

// matchesRow reports whether out contains an effective-flags table row for
// name/value/source, tolerant of tabwriter's dynamic column padding (exact
// spacing depends on the widest value in each column across the whole
// table) and the logx level label ("INFO  ") prefixing every line.
func matchesRow(out, name, value, source string) bool {
	pattern := `(?m)` + regexp.QuoteMeta(name) + `\s+` + regexp.QuoteMeta(value) + `\s+` + regexp.QuoteMeta(source) + `$`
	return regexp.MustCompile(pattern).MatchString(out)
}

func TestApplyConfig(t *testing.T) {
	t.Run("populates_unset_flags", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		if _, err := applyConfig(fs, map[string]any{"engineers": 7, "whole-team": false}, map[string]bool{}); err != nil {
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
		cliSet := snapshotSetFlags(fs)
		if _, err := applyConfig(fs, map[string]any{"engineers": 9}, cliSet); err != nil {
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
		if _, err := applyConfig(fs, map[string]any{"engineers": 5}, map[string]bool{}); err != nil {
			t.Fatal(err)
		}
		if !isFlagSet(fs, "engineers") {
			t.Error("isFlagSet(engineers) should be true after applyConfig")
		}
	})

	t.Run("unknown_key_errors", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		if _, err := applyConfig(fs, map[string]any{"enginers": 4}, map[string]bool{}); err == nil {
			t.Error("expected error for unknown key, got nil")
		}
	})

	t.Run("joins_multiple_key_errors", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		_, err := applyConfig(fs, map[string]any{"enginers": 4, "wole-team": true}, map[string]bool{})
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
		if _, err := applyConfig(fs, map[string]any{"percentile": []any{5, 25, 50}}, map[string]bool{}); err != nil {
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
		if _, err := applyConfig(fs, map[string]any{"config": "some-other.yaml", "engineers": 6}, map[string]bool{}); err != nil {
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
		out := captureStderr(t, func() {
			if err := ApplyConfig(fs, "/no/such/file.yaml"); err == nil {
				t.Error("expected error for missing file, got nil")
			}
		})
		if !matchesRow(out, "engineers", "3", "default") {
			t.Errorf("expected effective-flags report even on config load error, got %q", out)
		}
	})

	t.Run("empty_path_noop", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		if err := ApplyConfig(fs, ""); err != nil {
			t.Errorf("expected no error for empty path, got %v", err)
		}
	})

	t.Run("reports_default_source", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		out := captureStderr(t, func() {
			if err := ApplyConfig(fs, ""); err != nil {
				t.Fatal(err)
			}
		})
		if !matchesRow(out, "engineers", "3", "default") {
			t.Errorf("expected default-source log for untouched flag, got %q", out)
		}
	})

	t.Run("reports_config_file_source", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		path := writeYAML(t, "engineers: 9\n")
		out := captureStderr(t, func() {
			if err := ApplyConfig(fs, path); err != nil {
				t.Fatal(err)
			}
		})
		if !matchesRow(out, "engineers", "9", "config file") {
			t.Errorf("expected config-file-source log, got %q", out)
		}
	})

	t.Run("reports_cli_flag_source", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{"-engineers", "2"})
		out := captureStderr(t, func() {
			if err := ApplyConfig(fs, ""); err != nil {
				t.Fatal(err)
			}
		})
		if !matchesRow(out, "engineers", "2", "CLI flag") {
			t.Errorf("expected CLI-flag-source log, got %q", out)
		}
	})

	t.Run("logs_even_with_no_config_path", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		out := captureStderr(t, func() {
			if err := ApplyConfig(fs, ""); err != nil {
				t.Fatal(err)
			}
		})
		if !matchesRow(out, "db", "", "default") {
			t.Errorf("expected effective-flags report even with path==\"\", got %q", out)
		}
	})

	t.Run("cli_wins_reports_cli_flag_not_config_file", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{"-engineers", "2"})
		path := writeYAML(t, "engineers: 9\n")
		out := captureStderr(t, func() {
			if err := ApplyConfig(fs, path); err != nil {
				t.Fatal(err)
			}
		})
		if !matchesRow(out, "engineers", "2", "CLI flag") {
			t.Errorf("expected CLI value+source to win, got %q", out)
		}
	})

	t.Run("logs_partial_failure_attributes_succeeded_keys", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		path := writeYAML(t, "engineers: 5\nenginers: 4\n")
		out := captureStderr(t, func() {
			if err := ApplyConfig(fs, path); err == nil {
				t.Fatal("expected error for unknown key, got nil")
			}
		})
		if !matchesRow(out, "engineers", "5", "config file") {
			t.Errorf("expected succeeded key to still be attributed to config, got %q", out)
		}
	})

	t.Run("logs_header_and_intro_line", func(t *testing.T) {
		fs := newTestFlagSet()
		fs.Parse([]string{})
		out := captureStderr(t, func() {
			if err := ApplyConfig(fs, ""); err != nil {
				t.Fatal(err)
			}
		})
		if !strings.Contains(out, "effective flag values for this run:") {
			t.Errorf("expected intro line, got %q", out)
		}
		if !regexp.MustCompile(`(?m)FLAG\s+VALUE\s+SOURCE$`).MatchString(out) {
			t.Errorf("expected aligned header row, got %q", out)
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
