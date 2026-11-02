package domain

import "testing"

// Fängt: Pfad, Query oder Zugangsdaten einer Basis-URL in Fehlertext/Log (S-03).
func TestRedactURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://127.0.0.1:1234/v1", "http://127.0.0.1:1234"},
		{"http://localhost:1234/v1/", "http://localhost:1234"},
		{"https://api.notion.com", "https://api.notion.com"},
		{"https://user:pw@api.notion.com/v1?key=geheim#frag", "https://api.notion.com"},
		{"http://[::1]:1234/v1", "http://[::1]:1234"},
		{"localhost:1234", "(ungültige URL)"},
		{"", "(ungültige URL)"},
		{"http://localhost:1234\x7f", "(ungültige URL)"},
	}
	for _, c := range cases {
		if got := RedactURL(c.in); got != c.want {
			t.Errorf("RedactURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
