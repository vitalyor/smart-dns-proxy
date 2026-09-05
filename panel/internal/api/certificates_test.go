package api

import "testing"

func TestCertWarnLevel(t *testing.T) {
	cases := []struct {
		days  int
		due   bool
		level string
	}{
		{60, false, ""}, {21, false, ""},
		{20, true, "warn"}, {11, true, "warn"}, {4, true, "warn"},
		{3, true, "error"}, {0, true, "error"}, {-2, true, "error"},
	}
	for _, c := range cases {
		due, lvl := certWarnLevel(c.days)
		if due != c.due || lvl != c.level {
			t.Fatalf("certWarnLevel(%d) = %v/%q, want %v/%q", c.days, due, lvl, c.due, c.level)
		}
	}
}
