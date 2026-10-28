package provider

import "testing"

func TestCodexQuotaUsesProviderWindowDuration(t *testing.T) {
	for _, tt := range []struct {
		seconds int64
		want    string
	}{{5 * 60 * 60, "5 hours"}, {7 * 24 * 60 * 60, "7 days"}, {0, "Allowance"}} {
		if got := quotaDurationName(tt.seconds); got != tt.want {
			t.Errorf("quotaDurationName(%d) = %q, want %q", tt.seconds, got, tt.want)
		}
	}

	w := codexWindow{UsedPercent: 16, LimitWindowSecs: 7 * 24 * 60 * 60}
	if got := w.window(); got.Name != "7 days" || got.Used != 16 {
		t.Fatalf("Codex window = %+v", got)
	}
}

func TestCompactQuotaNumber(t *testing.T) {
	for _, tt := range []struct {
		in   float64
		want string
	}{{7, "7"}, {7.5, "7.5"}, {7.25, "7.25"}} {
		if got := compactNumber(tt.in); got != tt.want {
			t.Errorf("compactNumber(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
