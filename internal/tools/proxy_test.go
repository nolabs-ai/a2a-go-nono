package tools

import "testing"

func TestProxyDenial(t *testing.T) {
	tests := []struct {
		name   string
		code   int
		status string
		body   string
		want   bool
	}{
		{"reason in status line", 403, "403 Forbidden: host 169.254.169.254 is in the deny list", "", true},
		{"reason in body", 403, "403 Forbidden", "host example.com is not in the allowlist", true},
		{"ordinary 403 from a site", 403, "403 Forbidden", "you need to log in", false},
		{"L7 endpoint rule", 403, "403 Forbidden", `{"error":"Forbidden"}`, true},
		{"other JSON 403", 403, "403 Forbidden", `{"error":"token expired"}`, false},
		{"success", 200, "200 OK", "hello", false},
		{"deny text but not a 403", 200, "200 OK", "is in the deny list", false},
	}
	for _, tc := range tests {
		if _, got := proxyDenial(tc.code, tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
