package main

import (
	"testing"
)

func TestOriginValidation(t *testing.T) {
	tests := []struct {
		origin         string
		allowedOrigins []string
		expected       bool
	}{
		// Local loopback is always allowed
		{"", nil, true},
		{"http://localhost", nil, true},
		{"http://localhost:4000", nil, true},
		{"http://127.0.0.1", nil, true},
		{"http://127.0.0.1:8080", nil, true},
		{"http://[::1]", nil, true},
		{"http://[::1]:9000", nil, true},

		// Hostname prefix attacks must be rejected
		{"http://localhost.evil.com", nil, false},
		{"http://127.0.0.1.attacker.com", nil, false},
		{"http://evil-localhost.example.com:8080", nil, false},

		// Custom remote host/same-origin remote host must be rejected by default
		{"http://xxxx:4000", nil, false},
		{"http://192.168.1.100:4000", nil, false},

		// Custom remote host allowed via explicit matches
		{"http://xxxx:4000", []string{"xxxx:4000"}, true},
		{"http://xxxx:4000", []string{"xxxx"}, true},
		{"http://xxxx:4000", []string{"http://xxxx:4000"}, true},
		{"http://xxxx:4000", []string{"yyyy"}, false},
		{"http://192.168.1.100:4000", []string{"192.168.1.100"}, true},
	}

	for _, tc := range tests {
		got := checkOrigin(tc.origin, tc.allowedOrigins)
		if got != tc.expected {
			t.Errorf("checkOrigin(%q, %v) = %v; want %v", tc.origin, tc.allowedOrigins, got, tc.expected)
		}
	}
}
