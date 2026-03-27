package server_test

import (
	"testing"

	"github.com/na4ma4/hostcheck/internal/server"
)

func TestValidateHostname_Valid(t *testing.T) {
	// Helper to create a string of repeated characters
	repeatChar := func(c byte, n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = c
		}
		return string(b)
	}

	// Calculate max length hostname (253 chars total, 4 labels of 63 chars each = 252 chars + 3 dots = 255... need adjustment)
	// Actually: 63 + 1(dot) + 63 + 1(dot) + 63 + 1(dot) + 61 = 253
	maxHostname := repeatChar('a', 63) +
		"." + repeatChar('a', 63) +
		"." + repeatChar('a', 63) +
		"." + repeatChar('a', 61)

	tests := []struct {
		name     string
		hostname string
	}{
		{"simple domain", "example.com"},
		{"subdomain", "sub.example.com"},
		{"hyphen in label", "my-site.org"},
		{"multiple subdomains", "a.b.example.com"},
		{"numeric label", "example123.com"},
		{"mixed alphanumeric", "test-site123.example.com"},
		{"max length label (63 chars)", repeatChar('a', 63) + ".com"},
		{"max length hostname (253 chars)", maxHostname},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := server.ValidateHostname(tt.hostname)
			if err != nil {
				t.Errorf("ValidateHostname(%q) unexpected error: %v", tt.hostname, err)
			}
		})
	}
}

func TestValidateHostname_Invalid(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		wantErr  string
	}{
		{"empty string", "", "hostname is required"},
		{"single label", "example", "at least two labels"},
		{"starts with dot", ".example.com", "label 1 is empty"},
		{"starts with hyphen", "-example.com", "cannot start with a hyphen"},
		{"ends with hyphen", "example-.com", "cannot end with a hyphen"},
		{"label starts with hyphen", "example.-test.com", "cannot start with a hyphen"},
		{"label ends with hyphen", "example.test-.com", "cannot end with a hyphen"},
		{"invalid character underscore", "example_test.com", "invalid character '_'"},
		{"invalid character space", "example test.com", "invalid character ' '"},
		{"invalid character at sign", "example@test.com", "invalid character '@'"},
		{
			"too long label (64 chars)",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.example.com",
			"exceeds maximum length of 63",
		},
		{
			"too long hostname (254 chars)",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.com",
			"exceeds maximum length of 253",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := server.ValidateHostname(tt.hostname)
			if err == nil {
				t.Errorf("validateHostname(%q) expected error, got nil", tt.hostname)
				return
			}
			if !containsString(err.Error(), tt.wantErr) {
				t.Errorf("server.ValidateHostname(%q) error = %q, want containing %q",
					tt.hostname, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateHostname_URLStripping(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"https prefix", "https://example.com/path", false},
		{"http prefix", "http://example.com:8080", false},
		{"https with port", "https://example.com:443/api/v1", false},
		{"http with path and query", "http://example.com:8080/path?query=value", false},
		{
			"uppercase HTTP (stripped case-insensitively)",
			"HTTP://example.com", false,
		}, // The validator strips HTTP:// case-insensitively
		{
			"mixed case https",
			"HtTpS://example.com", false,
		}, // Should be stripped (case insensitive)
		{"empty after strip", "https://", true},
		{"only path after strip", "https:///path", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := server.ValidateHostname(tt.input)
			if tt.wantErr && err == nil {
				t.Errorf("server.ValidateHostname(%q) expected error, got nil", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("server.ValidateHostname(%q) unexpected error: %v", tt.input, err)
			}
		})
	}
}

func TestValidateHostname_RFC952_1123_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		wantErr  bool
		errMsg   string
	}{
		// RFC 952 originally restricted labels to start with a letter, but RFC 1123 relaxed this
		// to allow labels to start with a digit
		{"label starts with digit (RFC 1123 allowed)", "123example.com", false, ""},
		{"label is all digits", "123.456.com", false, ""},
		{"single character label", "a.bc", false, ""},
		{"double hyphen in middle", "my--site.example.com", false, ""},
		{"trailing dot (FQDN style)", "example.com.", true, ""}, // Our validator doesn't handle trailing dots well
		{"multiple dots", "example..com", true, "label 2 is empty"},
		{"domain with underscore (invalid per RFC)", "my_domain.example.com", true, "invalid character '_'"},
		{"IDN-like (punycode)", "xn--n3h.example.com", false, ""}, // Punycode is valid
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := server.ValidateHostname(tt.hostname)
			if tt.wantErr && err == nil {
				t.Errorf("server.ValidateHostname(%q) expected error, got nil", tt.hostname)
				return
			}
			if !tt.wantErr && err != nil {
				t.Errorf("server.ValidateHostname(%q) unexpected error: %v", tt.hostname, err)
				return
			}
			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !containsString(err.Error(), tt.errMsg) {
					t.Errorf(
						"server.ValidateHostname(%q) error = %q, want containing %q",
						tt.hostname,
						err.Error(),
						tt.errMsg,
					)
				}
			}
		})
	}
}

func TestValidateLabel(t *testing.T) {
	// Helper function to create a string of repeated characters
	repeatChar := func(c byte, n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = c
		}
		return string(b)
	}

	tests := []struct {
		name    string
		label   string
		index   int
		wantErr bool
		errMsg  string
	}{
		{"valid simple", "example", 0, false, ""},
		{"valid with hyphen", "my-site", 0, false, ""},
		{"valid numeric", "123", 0, false, ""},
		{"valid alphanumeric", "test123", 0, false, ""},
		{"empty label", "", 0, true, "label 1 is empty"},
		{"starts with hyphen", "-example", 0, true, "cannot start with a hyphen"},
		{"ends with hyphen", "example-", 0, true, "cannot end with a hyphen"},
		{"contains underscore", "ex_ample", 0, true, "invalid character '_'"},
		{"contains dot", "ex.ample", 0, true, "invalid character '.'"},
		{"contains space", "ex ample", 0, true, "invalid character ' '"},
		{"max length 63", repeatChar('a', 63), 0, false, ""},
		{"over max length 64", repeatChar('a', 64), 0, true, "exceeds maximum length of 63"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := server.ValidateLabel(tt.label, tt.index)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateLabel(%q, %d) expected error, got nil", tt.label, tt.index)
				return
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateLabel(%q, %d) unexpected error: %v", tt.label, tt.index, err)
				return
			}
			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !containsString(err.Error(), tt.errMsg) {
					t.Errorf(
						"ValidateLabel(%q, %d) error = %q, want containing %q",
						tt.label,
						tt.index,
						err.Error(),
						tt.errMsg,
					)
				}
			}
		})
	}
}

func TestIsAlphanumeric(t *testing.T) {
	tests := []struct {
		char     rune
		expected bool
	}{
		{'a', true},
		{'z', true},
		{'A', true},
		{'Z', true},
		{'0', true},
		{'9', true},
		{'-', false},
		{'_', false},
		{'.', false},
		{' ', false},
		{'@', false},
	}

	for _, tt := range tests {
		t.Run(string(tt.char), func(t *testing.T) {
			result := server.IsAlphanumeric(tt.char)
			if result != tt.expected {
				t.Errorf("server.IsAlphanumeric(%q) = %v, want %v", tt.char, result, tt.expected)
			}
		})
	}
}

func TestParseHostname(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantResult string
		wantErr    bool
	}{
		{"simple domain", "example.com", "example.com", false},
		{"https prefix", "https://example.com/path", "example.com", false},
		{"http with port", "http://example.com:8080", "example.com", false},
		{"subdomain", "sub.example.com", "sub.example.com", false},
		{"empty string", "", "", true},
		{"invalid single label", "example", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := server.ParseHostname(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("server.ParseHostname(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("server.ParseHostname(%q) unexpected error: %v", tt.input, err)
				return
			}
			if result != tt.wantResult {
				t.Errorf("server.ParseHostname(%q) = %q, want %q", tt.input, result, tt.wantResult)
			}
		})
	}
}

func TestIsValidURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"valid https url", "https://example.com/path", true},
		{"valid http url", "http://example.com", true},
		{"invalid url no hostname", "https://", false},
		{"not a url", "not-a-url", false},
		{"empty string", "", false},
		{"invalid hostname", "http://example", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := server.IsValidURL(tt.input)
			if result != tt.expected {
				t.Errorf("server.IsValidURL(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// helper function.
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
