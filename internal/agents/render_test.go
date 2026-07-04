package agents

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderCombined(t *testing.T) {
	tests := []struct {
		name    string
		general string
		coding  string
	}{
		{"typical newline-terminated inputs", "# General\nrule\n", "# Coding\nrule\n"},
		{"inputs without trailing newlines", "general", "coding"},
		{"empty inputs", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderCombined([]byte(tt.general), []byte(tt.coding))
			// Exact byte contract: header, blank line, general + "\n" + coding.
			want := CombinedHeader + "\n\n" + tt.general + "\n" + tt.coding
			if string(got) != want {
				t.Errorf("RenderCombined = %q, want %q", got, want)
			}
		})
	}
}

func TestRenderCombinedIsDeterministic(t *testing.T) {
	general := []byte("# General\nalways\n")
	coding := []byte("# Coding\nwhen coding\n")
	first := RenderCombined(general, coding)
	for i := 0; i < 3; i++ {
		if again := RenderCombined(general, coding); !bytes.Equal(first, again) {
			t.Fatalf("run %d produced different bytes:\n%q\nvs\n%q", i, first, again)
		}
	}
	// The header must never smuggle non-determinism in (a timestamp, a host).
	if strings.ContainsAny(CombinedHeader, "0123456789") {
		t.Errorf("CombinedHeader contains digits (a timestamp/version?): %q", CombinedHeader)
	}
}
