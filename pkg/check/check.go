// Package check defines the interface for host check plugins.
package check

import (
	"context"
	"time"
)

// Status represents the result status of a check.
type Status string

const (
	StatusPass    Status = "PASS"
	StatusFail    Status = "FAIL"
	StatusPartial Status = "PARTIAL"
	StatusWarn    Status = "WARN"
	StatusError   Status = "ERROR"
	StatusSkipped Status = "SKIPPED"
)

const DefaultTimeout = 60 * time.Second

type ResultTask struct {
	CheckName string `json:"check_name"`
	Status    Status `json:"status"`
	Message   string `json:"message"`
}

// Result represents the outcome of a check.
type Result struct {
	Name            string       `json:"name"`
	Status          Status       `json:"status"`
	Message         string       `json:"message"`
	Details         []string     `json:"details,omitempty"`
	Duration        string       `json:"duration"`
	Tasks           []ResultTask `json:"tasks,omitempty"`
	AdditionalHosts []string     `json:"additional_hosts,omitempty"`
}

// Check is the interface that all plugins must implement.
type Check interface {
	// Name returns the unique name of the check.
	Name() string

	// Description returns a human-readable description.
	Description() string

	// Run executes the check against the given hostname.
	Run(ctx context.Context, hostname string, cfg map[string]any) Result
}
