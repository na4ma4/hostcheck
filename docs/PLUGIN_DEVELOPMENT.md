# Plugin Development Guide

This guide explains how to create custom plugins for hostcheck.

## Overview

Hostcheck uses Go's plugin system (`-buildmode=plugin`) to load checks dynamically at runtime. Plugins are shared object files (`.so`) that implement the `Check` interface. This architecture allows you to extend hostcheck with custom checks without modifying the core application.

## The Check Interface

All plugins must implement the `check.Check` interface defined in `pkg/check/check.go`:

```go
// Check is the interface that all plugins must implement.
type Check interface {
    // Name returns the unique name of the check.
    Name() string

    // Description returns a human-readable description.
    Description() string

    // Run executes the check against the given hostname.
    Run(ctx context.Context, hostname string, cfg map[string]any) Result
}
```

### Result Structure

The `Run` method must return a `check.Result`:

```go
type Result struct {
    Name            string       `json:"name"`
    Status          Status       `json:"status"`
    Message         string       `json:"message"`
    Details         []string     `json:"details,omitempty"`
    Duration        string       `json:"duration"`
    Tasks           []ResultTask `json:"tasks,omitempty"`
    AdditionalHosts []string     `json:"additional_hosts,omitempty"`
}
```

### Status Values

| Status | Constant | Description |
|--------|----------|-------------|
| `PASS` | `check.StatusPass` | Check passed successfully |
| `FAIL` | `check.StatusFail` | Check failed |
| `PARTIAL` | `check.StatusPartial` | Check partially passed |
| `WARN` | `check.StatusWarn` | Check passed with warnings |
| `ERROR` | `check.StatusError` | Check encountered an error |
| `SKIPPED` | `check.StatusSkipped` | Check was skipped |

## Plugin Structure Requirements

### 1. Package Declaration

Your plugin must be in the `main` package:

```go
package main
```

### 2. Required Exports

Your plugin **must** export a variable named `Check` of type `check.Check`:

```go
// Check is the exported plugin instance.
var Check check.Check = NewMyCheck()
```

### 3. Interface Implementation

Your plugin struct must implement all three methods of the `Check` interface.

## Building Plugins

### Build Command

```bash
go build -buildmode=plugin -o myplugin.so ./path/to/plugin
```

### Example: Building the DNS Plugin

```bash
mkdir -p artifacts/plugins
go build -buildmode=plugin -o artifacts/plugins/dns.so ./plugins/dns
```

### Important Notes

- Plugins must be built with the same Go version as the main application
- Plugins must be built on the same platform/architecture as the host
- CGO is required for plugin support (`CGO_ENABLED=1`)
- Use `-trimpath` for reproducible builds

## Configuration

Plugins receive configuration through the `cfg map[string]any` parameter in the `Run` method. Configuration is defined in the main `hostcheck.yaml` file:

```yaml
plugins:
  directory: "./artifacts/plugins"
  
  # Per-plugin configuration
  dns:
    timeout: "60s"
    servers: ["8.8.8.8", "8.8.4.4"]
```

### Accessing Configuration

```go
func (c *MyCheck) Run(ctx context.Context, hostname string, cfg map[string]any) check.Result {
    // Get timeout with default
    timeout := 30 * time.Second
    if v, ok := cfg["timeout"]; ok {
        switch t := v.(type) {
        case string:
            if dur, err := time.ParseDuration(t); err == nil {
                timeout = dur
            }
        case int:
            timeout = time.Duration(t) * time.Second
        case time.Duration:
            timeout = t
        }
    }
    
    // ... rest of the check
}
```

## Example Plugin Skeleton

Here's a complete example of a minimal plugin:

```go
// Package main provides a basic check plugin for hostcheck.
package main

import (
    "context"
    "fmt"
    "log/slog"
    "time"

    "github.com/na4ma4/hostcheck/pkg/check"
)

// MyCheck implements the check.Check interface.
type MyCheck struct {
    logger *slog.Logger
}

// NewMyCheck creates a new check instance.
func NewMyCheck() *MyCheck {
    return &MyCheck{
        logger: slog.Default(),
    }
}

// Name returns the unique name of the check.
func (c *MyCheck) Name() string {
    return "mycheck"
}

// Description returns a human-readable description.
func (c *MyCheck) Description() string {
    return "Performs custom checks on the hostname"
}

// Run executes the check against the given hostname.
func (c *MyCheck) Run(ctx context.Context, hostname string, cfg map[string]any) check.Result {
    start := time.Now()
    details := make([]string, 0)
    tasks := make([]check.ResultTask, 0)

    c.logger.Debug("starting mycheck", "hostname", hostname)

    // Parse configuration
    timeout := 30 * time.Second
    if v, ok := cfg["timeout"]; ok {
        if t, ok := v.(int); ok {
            timeout = time.Duration(t) * time.Second
        }
    }

    // Create context with timeout
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    // Perform your check logic here
    // This is just an example - replace with your actual check
    
    // Example task 1: DNS resolution
    tasks = append(tasks, check.ResultTask{
        CheckName: "Resolution",
        Status:    check.StatusPass,
        Message:   fmt.Sprintf("Hostname %s resolved successfully", hostname),
    })
    details = append(details, "Resolution check passed")

    // Determine overall status
    status := check.StatusPass
    message := fmt.Sprintf("MyCheck passed for %s", hostname)

    // Check for context cancellation
    if ctx.Err() != nil {
        status = check.StatusError
        message = fmt.Sprintf("Check cancelled: %v", ctx.Err())
    }

    return check.Result{
        Name:     c.Name(),
        Status:   status,
        Message:  message,
        Details:  details,
        Duration: time.Since(start).Round(time.Millisecond).String(),
        Tasks:    tasks,
    }
}

// Check is the exported plugin instance.
var Check check.Check = NewMyCheck()

// Verify interface implementation at compile time.
var _ check.Check = (*MyCheck)(nil)
```

## Project Structure

Recommended directory structure for a new plugin:

```
plugins/
└── mycheck/
    ├── go.mod          # Module file (must import hostcheck/pkg/check)
    ├── go.sum          # Dependencies
    ├── main.go         # Plugin implementation
    └── README.md       # Plugin documentation
```

### go.mod Example

```go
module github.com/yourname/hostcheck-plugin-mycheck

go 1.24

require github.com/na4ma4/hostcheck v0.0.0 // Replace with actual version
```

## Testing Plugins

Since plugins cannot be loaded in tests, structure your code to allow testing the logic separately:

```go
// mycheck_test.go
package main

import (
    "context"
    "testing"
    
    "github.com/na4ma4/hostcheck/pkg/check"
)

func TestMyCheck_Run(t *testing.T) {
    c := NewMyCheck()
    
    result := c.Run(context.Background(), "example.com", nil)
    
    if result.Name != "mycheck" {
        t.Errorf("expected name 'mycheck', got %s", result.Name)
    }
    
    if result.Status != check.StatusPass {
        t.Errorf("expected status PASS, got %s", result.Status)
    }
}
```

## Best Practices

1. **Handle Context**: Always respect the context for cancellation and timeouts
2. **Log Appropriately**: Use structured logging with `slog` for debugging
3. **Return Meaningful Messages**: Provide clear, actionable messages in results
4. **Use Tasks**: Break down complex checks into tasks for better visibility
5. **Handle Errors Gracefully**: Return `StatusError` with explanatory messages
6. **Document Configuration**: Document expected configuration options
7. **Set Reasonable Defaults**: Provide sensible defaults for all configuration

## Troubleshooting

### Plugin Not Loading

- Ensure the plugin was built with the same Go version as hostcheck
- Verify the plugin exports a `Check` variable (capital C)
- Check that the plugin implements the full `Check` interface
- Ensure CGO is enabled (`CGO_ENABLED=1`)

### Symbol Lookup Failed

- Make sure the exported variable is named exactly `Check`
- Verify the type is `check.Check` (not a pointer to the struct)

### Type Assertion Failed

- Ensure your struct implements all interface methods
- Use the compile-time check: `var _ check.Check = (*MyCheck)(nil)`

## Further Reading

- [Go Plugin Package](https://pkg.go.dev/plugin)
- [Go Build Modes](https://pkg.go.dev/cmd/go#hdr-Build_modes)
- [Package check](../pkg/check/check.go) for interface definitions
