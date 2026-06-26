package util

import "testing"

func TestOrdinalSuffix(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "th"},
		{1, "st"},
		{2, "nd"},
		{3, "rd"},
		{4, "th"},
		{10, "th"},
		// 11–13 are the special "teen" cases that take "th" despite their
		// last digit.
		{11, "th"},
		{12, "th"},
		{13, "th"},
		{14, "th"},
		{21, "st"},
		{22, "nd"},
		{23, "rd"},
		{24, "th"},
		{100, "th"},
		{101, "st"},
		{111, "th"},
		{112, "th"},
		{113, "th"},
		{121, "st"},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			if got := OrdinalSuffix(tt.n); got != tt.want {
				t.Errorf("OrdinalSuffix(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}
