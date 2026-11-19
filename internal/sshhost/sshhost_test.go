package sshhost

import "testing"

// Valid rejects values that could inject an ssh option or shell text.
func TestValidRejects(t *testing.T) {
	for _, host := range []string{"-oProxyCommand=x", "host;id", "a b", "$(x)", "`id`", "", "a\nb"} {
		if Valid(host) {
			t.Errorf("Valid(%q) = true", host)
		}
	}
}

// Valid accepts host names and user@host.
func TestValidAccepts(t *testing.T) {
	for _, host := range []string{"user@host.example", "_h-1.example:22", "box", "10.0.0.1"} {
		if !Valid(host) {
			t.Errorf("Valid(%q) = false", host)
		}
	}
}
