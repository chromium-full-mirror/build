package digest

import (
	"regexp"
	"testing"
)

func TestIsHex(t *testing.T) {
	tests := []struct {
		input    byte
		expected bool
	}{
		// Valid digits
		{'0', true},
		{'5', true},
		{'9', true},

		// Valid lowercase hex
		{'a', true},
		{'f', true},

		// Invalid characters
		{'A', false}, // Function specifically targets lowercase
		{'g', false},
		{':', false},
		{'/', false},
		{' ', false},
	}

	for _, tt := range tests {
		result := IsHex(tt.input)
		if result != tt.expected {
			t.Errorf("IsHex(%q) = %v; want %v", tt.input, result, tt.expected)
		}
	}
}

// Benchmarks

const validSHA = "8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92"

var sha256Regexp = regexp.MustCompile(`^[a-f0-9]{64}$`)

func simpleDigestValidation(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		if !IsHex(s[i]) {
			return false
		}
	}
	return true
}

func regexDigestValidation(s string) bool {
	return sha256Regexp.MatchString(s)
}

func BenchmarkSimpleLoop(b *testing.B) {
	for b.Loop() {
		if !simpleDigestValidation(validSHA) {
			b.Error("expected true, got false")
		}
	}
}

func BenchmarkRegexp(b *testing.B) {
	for b.Loop() {
		if !regexDigestValidation(validSHA) {
			b.Error("expected true, got false")
		}
	}
}
