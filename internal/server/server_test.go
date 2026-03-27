package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/na4ma4/go-healthcheck"
	check "github.com/na4ma4/go-hostcheck-interface"
	"github.com/na4ma4/go-slogtool"
	"github.com/na4ma4/hostcheck/internal/plugin"
	"github.com/na4ma4/hostcheck/internal/server"
)

// mockCheck implements check.Check for testing.
type mockCheck struct {
	name        string
	description string
	result      check.Result
}

type concurrencyCheck struct {
	name        string
	description string
	running     *atomic.Int64
	maxRunning  *atomic.Int64
	holdFor     time.Duration
}

func (m *mockCheck) Name() string {
	return m.name
}

func (m *mockCheck) Description() string {
	return m.description
}

func (m *mockCheck) Run(_ context.Context, _ string, _ map[string]any) check.Result {
	return m.result
}

func (m *mockCheck) Version() []byte {
	// Return dummy version info in cliversion format as a JSON string
	versionInfo := map[string]string{
		"version": "1.0.0",
	}
	buf, _ := json.Marshal(versionInfo)
	return buf
}

func (c *concurrencyCheck) Name() string {
	return c.name
}

func (c *concurrencyCheck) Description() string {
	return c.description
}

func (c *concurrencyCheck) Version() []byte {
	// Return dummy version info in cliversion format as a JSON string
	versionInfo := map[string]string{
		"version": "1.0.0",
	}
	buf, _ := json.Marshal(versionInfo)
	return buf
}

func (c *concurrencyCheck) Run(_ context.Context, _ string, _ map[string]any) check.Result {
	current := c.running.Add(1)
	for {
		maxCurrent := c.maxRunning.Load()
		if current <= maxCurrent {
			break
		}
		if c.maxRunning.CompareAndSwap(maxCurrent, current) {
			break
		}
	}

	time.Sleep(c.holdFor)
	c.running.Add(-1)

	return check.Result{Name: c.name, Status: check.StatusPass, Message: "ok"}
}

func newTestServer(t *testing.T, checks ...check.Check) *server.Server {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hc := healthcheck.NewCore()
	logmgr := slogtool.NewSlogManager(slogtool.WithTextHandler(), slogtool.WithWriter(io.Discard))
	registry := plugin.NewRegistry(logger)

	for _, c := range checks {
		registry.Register(c)
	}

	return server.NewServer(registry, logger, hc, server.Config{
		RateLimit:     100,
		MaxConcurrent: 10,
		MaxTimeout:    30 * time.Second,
	}, logmgr)
}

//nolint:gocognit // TestHandleCheckList tests that HandleCheckList correctly returns the list of checks.
func TestHandleCheckList(t *testing.T) {
	tests := []struct {
		name      string
		checks    []check.Check
		wantCount int
		wantNames []string
	}{
		{
			name:      "empty registry",
			checks:    []check.Check{},
			wantCount: 0,
			wantNames: []string{},
		},
		{
			name: "single check",
			checks: []check.Check{
				&mockCheck{name: "dns", description: "DNS check"},
			},
			wantCount: 1,
			wantNames: []string{"dns"},
		},
		{
			name: "multiple checks",
			checks: []check.Check{
				&mockCheck{name: "dns", description: "DNS check"},
				&mockCheck{name: "ssl", description: "SSL check"},
				&mockCheck{name: "http", description: "HTTP check"},
			},
			wantCount: 3,
			wantNames: []string{"dns", "ssl", "http"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, tt.checks...)

			req := httptest.NewRequest(http.MethodGet, "/api/checks", nil)
			w := httptest.NewRecorder()

			srv.HandleCheckList(w, req)

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("HandleCheckList() status = %d, want %d", resp.StatusCode, http.StatusOK)
			}

			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("HandleCheckList() Content-Type = %q, want %q", ct, "application/json")
			}

			var result struct {
				Checks []server.CheckInfo `json:"checks"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			if len(result.Checks) != tt.wantCount {
				t.Errorf("HandleCheckList() returned %d checks, want %d", len(result.Checks), tt.wantCount)
			}

			// Check that expected names are present
			foundNames := make(map[string]bool)
			for _, c := range result.Checks {
				foundNames[c.Name] = true
			}
			for _, name := range tt.wantNames {
				if !foundNames[name] {
					t.Errorf("HandleCheckList() missing check %q", name)
				}
			}
		})
	}
}

func TestHandleCheck_InvalidHostname(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		method   string
		body     string
	}{
		{
			name:     "empty hostname GET",
			hostname: "",
			method:   http.MethodGet,
		},
		{
			name:     "empty hostname POST",
			hostname: "",
			method:   http.MethodPost,
			body:     `{"hostname": ""}`,
		},
		{
			name:     "single label GET",
			hostname: "example",
			method:   http.MethodGet,
		},
		{
			name:     "starts with dot GET",
			hostname: ".example.com",
			method:   http.MethodGet,
		},
		{
			name:     "starts with hyphen GET",
			hostname: "-example.com",
			method:   http.MethodGet,
		},
		{
			name:     "invalid characters GET",
			hostname: "example_test.com",
			method:   http.MethodGet,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, &mockCheck{
				name:        "test",
				description: "test check",
				result:      check.Result{Name: "test", Status: check.StatusPass, Message: "ok"},
			})

			var req *http.Request
			if tt.method == http.MethodPost {
				req = httptest.NewRequest(http.MethodPost, "/api/check", strings.NewReader(tt.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(http.MethodGet, "/api/check?hostname="+url.QueryEscape(tt.hostname), nil)
			}

			w := httptest.NewRecorder()
			srv.HandleCheck(w, req)

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("HandleCheck() status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
			}

			var result server.ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			if result.Error == "" {
				t.Error("HandleCheck() expected error message in response")
			}
		})
	}
}

//nolint:gocognit // TestHandleCheck_ValidHostname tests that HandleCheck correctly processes valid hostnames and returns expected results.
func TestHandleCheck_ValidHostname(t *testing.T) {
	tests := []struct {
		name       string
		hostname   string
		method     string
		body       string
		checks     []check.Check
		wantStatus int
		wantPassed int
		wantFailed int
	}{
		{
			name:     "simple domain GET",
			hostname: "example.com",
			method:   http.MethodGet,
			checks: []check.Check{
				&mockCheck{
					name:        "dns",
					description: "DNS check",
					result:      check.Result{Name: "dns", Status: check.StatusPass, Message: "DNS OK"},
				},
			},
			wantStatus: http.StatusOK,
			wantPassed: 1,
		},
		{
			name:     "simple domain POST",
			hostname: "example.com",
			method:   http.MethodPost,
			body:     `{"hostname": "example.com"}`,
			checks: []check.Check{
				&mockCheck{
					name:        "dns",
					description: "DNS check",
					result:      check.Result{Name: "dns", Status: check.StatusPass, Message: "DNS OK"},
				},
			},
			wantStatus: http.StatusOK,
			wantPassed: 1,
		},
		{
			name:     "subdomain GET",
			hostname: "sub.example.com",
			method:   http.MethodGet,
			checks: []check.Check{
				&mockCheck{
					name:        "dns",
					description: "DNS check",
					result:      check.Result{Name: "dns", Status: check.StatusPass, Message: "DNS OK"},
				},
				&mockCheck{
					name:        "ssl",
					description: "SSL check",
					result:      check.Result{Name: "ssl", Status: check.StatusPass, Message: "SSL OK"},
				},
			},
			wantStatus: http.StatusOK,
			wantPassed: 2,
		},
		{
			name:     "URL with https prefix",
			hostname: "https://example.com/path",
			method:   http.MethodGet,
			checks: []check.Check{
				&mockCheck{
					name:        "dns",
					description: "DNS check",
					result:      check.Result{Name: "dns", Status: check.StatusPass, Message: "DNS OK"},
				},
			},
			wantStatus: http.StatusOK,
			wantPassed: 1,
		},
		{
			name:     "check fails",
			hostname: "example.com",
			method:   http.MethodGet,
			checks: []check.Check{
				&mockCheck{
					name:        "dns",
					description: "DNS check",
					result:      check.Result{Name: "dns", Status: check.StatusFail, Message: "DNS failed"},
				},
			},
			wantStatus: http.StatusOK,
			wantFailed: 1,
		},
		{
			name:     "mixed results",
			hostname: "example.com",
			method:   http.MethodGet,
			checks: []check.Check{
				&mockCheck{
					name:        "dns",
					description: "DNS check",
					result:      check.Result{Name: "dns", Status: check.StatusPass, Message: "DNS OK"},
				},
				&mockCheck{
					name:        "ssl",
					description: "SSL check",
					result:      check.Result{Name: "ssl", Status: check.StatusFail, Message: "SSL failed"},
				},
				&mockCheck{
					name:        "http",
					description: "HTTP check",
					result:      check.Result{Name: "http", Status: check.StatusWarn, Message: "HTTP warning"},
				},
			},
			wantStatus: http.StatusOK,
			wantPassed: 1,
			wantFailed: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, tt.checks...)

			var req *http.Request
			if tt.method == http.MethodPost {
				req = httptest.NewRequest(http.MethodPost, "/api/check", strings.NewReader(tt.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(http.MethodGet, "/api/check?hostname="+url.QueryEscape(tt.hostname), nil)
			}

			w := httptest.NewRecorder()
			srv.HandleCheck(w, req)

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("HandleCheck() status = %d, want %d", resp.StatusCode, tt.wantStatus)
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("Response body: %s", string(body))
				return
			}

			var result server.CheckResponse
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			// Verify hostname is stripped of URL prefix
			expectedHostname := tt.hostname
			if strings.HasPrefix(strings.ToLower(expectedHostname), "https://") {
				expectedHostname = expectedHostname[8:] // Remove https://
			}
			if idx := strings.Index(expectedHostname, "/"); idx >= 0 {
				expectedHostname = expectedHostname[:idx]
			}

			if result.Hostname != expectedHostname {
				t.Errorf("HandleCheck() hostname = %q, want %q", result.Hostname, expectedHostname)
			}

			// Check that the response contains results
			if len(result.Results) != len(tt.checks) {
				t.Errorf("HandleCheck() returned %d results, want %d", len(result.Results), len(tt.checks))
			}

			if result.Summary.Passed != tt.wantPassed {
				t.Errorf("HandleCheck() passed = %d, want %d", result.Summary.Passed, tt.wantPassed)
			}

			if result.Summary.Failed != tt.wantFailed {
				t.Errorf("HandleCheck() failed = %d, want %d", result.Summary.Failed, tt.wantFailed)
			}
		})
	}
}

func TestHandleCheck_NoChecksAvailable(t *testing.T) {
	srv := newTestServer(t) // No checks registered

	req := httptest.NewRequest(http.MethodGet, "/api/check?hostname=example.com", nil)
	w := httptest.NewRecorder()

	srv.HandleCheck(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("HandleCheck() status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	var result server.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !strings.Contains(result.Error, "no valid checks") {
		t.Errorf("HandleCheck() error = %q, want containing 'no valid checks'", result.Error)
	}
}

func TestHandleCheck_MethodNotAllowed(t *testing.T) {
	srv := newTestServer(t, &mockCheck{
		name:        "dns",
		description: "DNS check",
		result:      check.Result{Name: "dns", Status: check.StatusPass, Message: "DNS OK"},
	})

	req := httptest.NewRequest(http.MethodPut, "/api/check?hostname=example.com", nil)
	w := httptest.NewRecorder()

	srv.HandleCheck(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("HandleCheck() status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}

	var result server.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !strings.Contains(result.Error, "method not allowed") {
		t.Errorf("HandleCheck() error = %q, want containing 'method not allowed'", result.Error)
	}
}

func TestHandleCheckSSE_MethodNotAllowed(t *testing.T) {
	srv := newTestServer(t, &mockCheck{
		name:        "dns",
		description: "DNS check",
		result:      check.Result{Name: "dns", Status: check.StatusPass, Message: "DNS OK"},
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/check/sse?hostname=example.com", nil)
	w := httptest.NewRecorder()

	srv.HandleCheckSSE(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("HandleCheckSSE() status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}

	var result server.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !strings.Contains(result.Error, "method not allowed") {
		t.Errorf("HandleCheckSSE() error = %q, want containing 'method not allowed'", result.Error)
	}
}

func TestHandleCheck_FilterChecks(t *testing.T) {
	checks := []check.Check{
		&mockCheck{
			name:        "dns",
			description: "DNS check",
			result:      check.Result{Name: "dns", Status: check.StatusPass, Message: "DNS OK"},
		},
		&mockCheck{
			name:        "ssl",
			description: "SSL check",
			result:      check.Result{Name: "ssl", Status: check.StatusPass, Message: "SSL OK"},
		},
		&mockCheck{
			name:        "http",
			description: "HTTP check",
			result:      check.Result{Name: "http", Status: check.StatusPass, Message: "HTTP OK"},
		},
	}

	srv := newTestServer(t, checks...)

	// Request only DNS check
	req := httptest.NewRequest(http.MethodGet, "/api/check?hostname=example.com&check=dns", nil)
	w := httptest.NewRecorder()

	srv.HandleCheck(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("HandleCheck() status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var result server.CheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(result.Results) != 1 {
		t.Errorf("HandleCheck() returned %d results, want 1", len(result.Results))
	}

	if len(result.Results) > 0 && result.Results[0].Name != "dns" {
		t.Errorf("HandleCheck() returned check %q, want 'dns'", result.Results[0].Name)
	}
}

func TestHandleCheck_RespectsMaxConcurrent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hc := healthcheck.NewCore()
	logmgr := slogtool.NewSlogManager(slogtool.WithTextHandler(), slogtool.WithWriter(io.Discard))
	registry := plugin.NewRegistry(logger)

	var running atomic.Int64
	var maxRunning atomic.Int64

	checks := []check.Check{
		&concurrencyCheck{
			name: "c1", description: "concurrency test", running: &running,
			maxRunning: &maxRunning, holdFor: 80 * time.Millisecond,
		},
		&concurrencyCheck{
			name: "c2", description: "concurrency test", running: &running,
			maxRunning: &maxRunning, holdFor: 80 * time.Millisecond,
		},
		&concurrencyCheck{
			name: "c3", description: "concurrency test", running: &running,
			maxRunning: &maxRunning, holdFor: 80 * time.Millisecond,
		},
		&concurrencyCheck{
			name: "c4", description: "concurrency test", running: &running,
			maxRunning: &maxRunning, holdFor: 80 * time.Millisecond,
		},
	}

	for _, c := range checks {
		registry.Register(c)
	}

	srv := server.NewServer(registry, logger, hc, server.Config{
		RateLimit:     100,
		MaxConcurrent: 1,
		MaxTimeout:    30 * time.Second,
	}, logmgr)

	req := httptest.NewRequest(http.MethodGet, "/api/check?hostname=example.com", nil)
	w := httptest.NewRecorder()

	srv.HandleCheck(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("HandleCheck() status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var result server.CheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(result.Results) != len(checks) {
		t.Fatalf("HandleCheck() returned %d results, want %d", len(result.Results), len(checks))
	}

	if observed := maxRunning.Load(); observed > 1 {
		t.Fatalf("max concurrent checks observed = %d, want <= 1", observed)
	}
}

//nolint:gocognit // TestRateLimitMiddleware tests that the rate limiting middleware correctly limits requests.
func TestParseCheckRequest(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct {
		name         string
		method       string
		queryParams  string
		body         string
		wantHostname string
		wantChecks   []string
		wantTimeout  int
		wantErr      bool
	}{
		{
			name:         "GET with all params",
			method:       http.MethodGet,
			queryParams:  "?hostname=example.com&check=dns&check=ssl&timeout=30",
			wantHostname: "example.com",
			wantChecks:   []string{"dns", "ssl"},
			wantTimeout:  30,
		},
		{
			name:         "POST with JSON body",
			method:       http.MethodPost,
			body:         `{"hostname": "example.com", "checks": ["dns"], "timeout": 60}`,
			wantHostname: "example.com",
			wantChecks:   []string{"dns"},
			wantTimeout:  60,
		},
		{
			name:         "GET with hostname only",
			method:       http.MethodGet,
			queryParams:  "?hostname=example.com",
			wantHostname: "example.com",
			wantChecks:   nil,
			wantTimeout:  0,
		},
		{
			name:         "POST with empty body falls back to query",
			method:       http.MethodPost,
			queryParams:  "?hostname=example.com",
			body:         ``,
			wantHostname: "example.com",
			wantChecks:   nil,
			wantTimeout:  0,
		},
		{
			name:    "POST with invalid JSON",
			method:  http.MethodPost,
			body:    `{invalid json}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			if tt.method == http.MethodPost {
				req = httptest.NewRequest(http.MethodPost, "/api/check"+tt.queryParams, strings.NewReader(tt.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(http.MethodGet, "/api/check"+tt.queryParams, nil)
			}

			result, err := srv.ParseCheckRequest(req)

			if tt.wantErr {
				if err == nil {
					t.Error("parseCheckRequest() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("parseCheckRequest() unexpected error: %v", err)
				return
			}

			if result.Hostname != tt.wantHostname {
				t.Errorf("parseCheckRequest() hostname = %q, want %q", result.Hostname, tt.wantHostname)
			}

			if len(result.Checks) != len(tt.wantChecks) {
				t.Errorf("parseCheckRequest() checks = %v, want %v", result.Checks, tt.wantChecks)
			}

			if result.Timeout != tt.wantTimeout {
				t.Errorf("parseCheckRequest() timeout = %d, want %d", result.Timeout, tt.wantTimeout)
			}
		})
	}
}

func TestParseCheckRequest_PostBodyTooLarge(t *testing.T) {
	srv := newTestServer(t)

	// Must exceed the 1 MiB body cap configured by the server.
	oversizedHostname := strings.Repeat("a", 1024*1024+1)
	body := `{"hostname":"` + oversizedHostname + `"}`

	req := httptest.NewRequest(http.MethodPost, "/api/check", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	_, err := srv.ParseCheckRequest(req)
	if err == nil {
		t.Fatal("ParseCheckRequest() expected error for oversized POST body, got nil")
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	// Create a server with a low rate limit
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hc := healthcheck.NewCore()
	logmgr := slogtool.NewSlogManager(slogtool.WithTextHandler(), slogtool.WithWriter(io.Discard))
	registry := plugin.NewRegistry(logger)

	registry.Register(&mockCheck{
		name:        "test",
		description: "test check",
		result:      check.Result{Name: "test", Status: check.StatusPass, Message: "ok"},
	})

	srv := server.NewServer(registry, logger, hc, server.Config{
		RateLimit:     2, // Very low rate limit (2 req/sec)
		MaxConcurrent: 10,
		MaxTimeout:    30 * time.Second,
	}, logmgr)

	// Send multiple requests rapidly - some should succeed, some should be rate limited
	successCount := 0
	rateLimitedCount := 0

	for range 30 {
		req := httptest.NewRequest(http.MethodGet, "/api/check?hostname=example.com", nil)
		w := httptest.NewRecorder()

		handler := srv.RateLimitMiddleware(srv.HandleCheck)
		handler(w, req)

		resp := w.Result()
		_ = resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			successCount++
		case http.StatusTooManyRequests:
			rateLimitedCount++
		default:
			t.Errorf("Unexpected status code: %d", resp.StatusCode)
		}
	}

	// We expect at least some requests to succeed and some to be rate limited
	if successCount == 0 {
		t.Error("Expected at least some successful requests")
	}
	if rateLimitedCount == 0 {
		t.Error("Expected at least some rate-limited requests")
	}
}
