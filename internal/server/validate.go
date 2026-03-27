package server

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ValidateHostname validates a hostname according to RFC 952/1123.
// It strips http:// or https:// prefixes, trailing paths, and ports before validation.
// Returns an error with a descriptive message if validation fails.
func ValidateHostname(input string) error {
	if input == "" {
		return errors.New("hostname is required")
	}

	hostname := input

	// Strip http:// or https:// prefix if present
	if strings.HasPrefix(strings.ToLower(hostname), "http://") {
		hostname = hostname[7:]
	} else if strings.HasPrefix(strings.ToLower(hostname), "https://") {
		hostname = hostname[8:]
	}

	// Strip trailing path (e.g., /foo/bar)
	if idx := strings.Index(hostname, "/"); idx >= 0 {
		hostname = hostname[:idx]
	}

	// Strip port if present (e.g., :8080)
	if idx := strings.LastIndex(hostname, ":"); idx >= 0 {
		hostname = hostname[:idx]
	}

	// Total hostname must be max 253 characters
	if len(hostname) > RFC952HostnameMaxLength {
		return fmt.Errorf("hostname exceeds maximum length of %d characters (got %d)",
			RFC952HostnameMaxLength, len(hostname))
	}

	if len(hostname) == 0 {
		return errors.New("hostname is empty after stripping URL components")
	}

	// Split into labels
	labels := strings.Split(hostname, ".")

	// At least 2 labels required (e.g., example.com)
	if len(labels) < RFC952MinLabels {
		return fmt.Errorf("hostname must have at least two labels (e.g., example.com), got %d label(s)",
			len(labels))
	}

	// Validate each label
	for i, label := range labels {
		if err := ValidateLabel(label, i); err != nil {
			return err
		}
	}

	return nil
}

const (
	RFC952LabelMaxLength    = 63
	RFC952HostnameMaxLength = 253
	RFC952MinLabels         = 2
)

// ValidateLabel validates a single label according to RFC 952/1123.
func ValidateLabel(label string, index int) error {
	// Each label must be 1-63 characters
	if len(label) == 0 {
		return fmt.Errorf("label %d is empty", index+1)
	}
	if len(label) > RFC952LabelMaxLength {
		return fmt.Errorf("label %d exceeds maximum length of %d characters (got %d)",
			index+1, RFC952LabelMaxLength, len(label))
	}

	// Cannot start with hyphen
	if label[0] == '-' {
		return fmt.Errorf("label %d cannot start with a hyphen", index+1)
	}

	// Cannot end with hyphen
	if label[len(label)-1] == '-' {
		return fmt.Errorf("label %d cannot end with a hyphen", index+1)
	}

	// Only alphanumeric and hyphens allowed
	for j, r := range label {
		if !IsAlphanumeric(r) && r != '-' {
			return fmt.Errorf(
				"label %d contains invalid character '%c' at position %d (only alphanumeric and hyphens allowed)",
				index+1,
				r,
				j+1,
			)
		}
	}

	return nil
}

// IsAlphanumeric checks if a rune is an ASCII letter or digit.
func IsAlphanumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// ParseHostname extracts and validates a hostname from the input string.
// Returns the cleaned hostname or an error.
func ParseHostname(input string) (string, error) {
	if input == "" {
		return "", errors.New("hostname is required")
	}

	hostname := input

	// Strip http:// or https:// prefix if present
	if strings.HasPrefix(strings.ToLower(hostname), "http://") {
		hostname = hostname[7:]
	} else if strings.HasPrefix(strings.ToLower(hostname), "https://") {
		hostname = hostname[8:]
	}

	// Strip trailing path (e.g., /foo/bar)
	if idx := strings.Index(hostname, "/"); idx >= 0 {
		hostname = hostname[:idx]
	}

	// Strip port if present (e.g., :8080)
	if idx := strings.LastIndex(hostname, ":"); idx >= 0 {
		hostname = hostname[:idx]
	}

	// Validate the cleaned hostname
	if err := ValidateHostname(hostname); err != nil {
		return "", err
	}

	return hostname, nil
}

// IsValidURL checks if the input is a valid URL with a hostname.
func IsValidURL(input string) bool {
	u, err := url.Parse(input)
	if err != nil {
		return false
	}

	if u.Hostname() == "" {
		return false
	}

	return ValidateHostname(u.Hostname()) == nil
}
