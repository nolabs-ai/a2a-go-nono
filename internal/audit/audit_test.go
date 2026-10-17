package audit

import (
	"errors"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name    string
		result  map[string]any
		err     error
		outcome string
	}{
		{"ok", map[string]any{"status": "ok"}, nil, Allowed},
		{"nono block", map[string]any{"status": "error", "sandbox": "nono", "error": "denied"}, nil, Denied},
		{"plain error", map[string]any{"status": "error", "error": "no such path"}, nil, Failed},
		{"go error", nil, errors.New("boom"), Failed},
	}
	for _, tc := range tests {
		if got, _ := Classify(tc.result, tc.err); got != tc.outcome {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.outcome)
		}
	}
}

func TestFormatArgsTruncatesAndSorts(t *testing.T) {
	got := FormatArgs(map[string]any{"b": strings.Repeat("x", 500), "a": 1})
	if !strings.HasPrefix(got, "a=1 b=") || !strings.HasSuffix(got, "…") || len(got) > 200 {
		t.Errorf("unexpected: %q", got)
	}
}
