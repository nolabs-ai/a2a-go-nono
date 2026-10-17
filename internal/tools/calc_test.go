package tools

import "testing"

func TestEvalExpr(t *testing.T) {
	ok := map[string]float64{
		"2 + 2 * 10":   22,
		"(3 ** 4) / 9": 9,
		"2 ** 3 ** 2":  512,
		"-2 ** 2":      -4,
		"7 // 2":       3,
		"7 % 4":        3,
		"2 * -3":       -6,
	}
	for in, want := range ok {
		got, err := evalExpr(in)
		if err != nil || got != want {
			t.Errorf("evalExpr(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "1 +", "abc", "__import__('os')", "1 / 0", "(1", "1 2"} {
		if v, err := evalExpr(in); err == nil {
			t.Errorf("evalExpr(%q) = %v; want error", in, v)
		}
	}
}
