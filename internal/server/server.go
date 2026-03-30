// Package server provides the HTTP server for the hostcheck service.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/dosquad/go-cliversion"
	"github.com/na4ma4/go-contextual"
	"github.com/na4ma4/go-healthcheck"
	check "github.com/na4ma4/go-hostcheck-interface"
	"github.com/na4ma4/go-slogtool"
	"github.com/na4ma4/hostcheck/internal/plugin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/encoding/protojson"
)

//go:embed index.html
var staticFS embed.FS

const (
	shutdownTimeout     = 10 * time.Second
	readTimeout         = 30 * time.Second
	idleTimeout         = 120 * time.Second
	readHeaderTimeout   = 10 * time.Second
	writeTimeout        = 60 * time.Second
	keepaliveInterval   = 15 * time.Second
	defaultMaxTimeout   = 300 * time.Second
	maxRequestBodyBytes = 1 << 20 // 1 MiB
)

// Server represents the HTTP server.
type Server struct {
	registry       *plugin.Registry
	logger         *slog.Logger
	hc             healthcheck.Health
	limiter        *rate.Limiter
	metrics        *Metrics
	checkSemaphore chan struct{}
	maxTimeout     time.Duration
	logmgr         slogtool.LogManager
}

// Config holds configuration options for the server.
type Config struct {
	RateLimit     float64       // Requests per second (default: 10)
	MaxConcurrent int           // Max concurrent checks (default: 4 or NumCPU)
	MaxTimeout    time.Duration // Maximum allowed timeout per request (default: 300s)
}

const (
	defaultMaxConcurrent = 4
	minRateLimit         = 10
)

// NewServer creates a new server instance.
func NewServer(
	registry *plugin.Registry,
	logger *slog.Logger,
	hc healthcheck.Health,
	cfg Config,
	logmgr slogtool.LogManager,
) *Server {
	// Set defaults
	rateLimit := cfg.RateLimit
	if rateLimit < minRateLimit {
		rateLimit = minRateLimit
	}

	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = max(defaultMaxConcurrent, runtime.NumCPU())
	}

	maxTimeout := cfg.MaxTimeout
	if maxTimeout <= 0 {
		maxTimeout = defaultMaxTimeout
	}

	return &Server{
		registry:       registry,
		logger:         logger,
		hc:             hc,
		limiter:        rate.NewLimiter(rate.Limit(rateLimit), int(rateLimit)),
		checkSemaphore: make(chan struct{}, maxConcurrent),
		maxTimeout:     maxTimeout,
		logmgr:         logmgr,
		metrics:        NewMetrics(),
	}
}

// CheckRequest represents the JSON request body for check endpoints.
type CheckRequest struct {
	Hostname string   `json:"hostname"`
	Checks   []string `json:"checks"`
	Timeout  int      `json:"timeout,omitempty"` // timeout in seconds
}

var (
	ErrInvalidRequest = errors.New("invalid request")
	ErrFallthrough    = errors.New("request parsing fallthrough")
)

// ParseCheckRequest extracts check parameters from either POST JSON body or GET query parameters.
func (s *Server) ParseCheckRequest(r *http.Request) (*CheckRequest, error) {
	req := &CheckRequest{}

	// Try to parse from POST body first
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)
		rq, err := parseCheckRequestPost(r)
		if err != nil && !errors.Is(err, ErrFallthrough) {
			return nil, err
		} else if err == nil {
			return rq, nil
		}
	}

	// Fall back to GET query parameters
	req.Hostname = r.URL.Query().Get("hostname")
	req.Checks = r.URL.Query()["check"]

	if timeoutStr := r.URL.Query().Get("timeout"); timeoutStr != "" {
		if timeout, err := strconv.Atoi(timeoutStr); err == nil {
			req.Timeout = timeout
		}
	}

	return req, nil
}

func parseCheckRequestPost(r *http.Request) (*CheckRequest, error) {
	req := &CheckRequest{}

	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		// If body parsing fails, check if it's empty - fall back to query params
		if !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: JSON body failed to parse: %w", ErrInvalidRequest, err)
		}

		return nil, ErrFallthrough
	}

	// POST body was parsed successfully, but also check for timeout query param
	if timeoutStr := r.URL.Query().Get("timeout"); timeoutStr != "" {
		if timeout, err := strconv.Atoi(timeoutStr); err == nil {
			req.Timeout = timeout
		}
	}

	return req, nil
}

// getTimeout returns the effective timeout capped at maxTimeout.
func (s *Server) getTimeout(reqTimeout int) time.Duration {
	timeout := check.DefaultTimeout

	if reqTimeout > 0 {
		timeout = time.Duration(reqTimeout) * time.Second
	}

	if timeout > s.maxTimeout {
		timeout = s.maxTimeout
	}

	return timeout
}

// Run starts the HTTP server.
func (s *Server) Run(ctx context.Context, addr string) error {
	defer s.hc.Get("webserver").Start().Stop()
	mux := http.NewServeMux()

	mux.HandleFunc("/", s.HandleIndex)
	mux.HandleFunc("/api/check", s.RateLimitMiddleware(s.HandleCheck))
	mux.HandleFunc("/api/check/sse", s.RateLimitMiddleware(s.HandleCheckSSE))
	mux.HandleFunc("/api/checks", s.RateLimitMiddleware(s.HandleCheckList))

	// Log level endpoint
	mux.HandleFunc("/api/log/level", s.HandleLogLevel)

	// Health check endpoint
	mux.HandleFunc("/health", healthcheck.Handler(s.hc))

	// Version endpoint
	mux.HandleFunc("/version", s.HandleVersion)

	// Prometheus metrics endpoint
	mux.Handle("/metrics", promhttp.HandlerFor(s.metrics.Registry, promhttp.HandlerOpts{}))

	addAdditionalMux(mux)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadTimeout:       readTimeout,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		item := s.hc.Get("webserver.Context").Start()
		defer item.Stop()
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			item.Error(err)
			return err
		}

		return nil
	})

	eg.Go(func() error {
		item := s.hc.Get("webserver.Listen").Start()
		s.logger.Info("server listening", slog.String("listen", addr))

		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			item.Error(err)
			return err
		}

		return nil
	})

	return eg.Wait()
}

// acquireSemaphore acquires a slot in the check semaphore.
func (s *Server) acquireSemaphore(ctx context.Context) error {
	select {
	case s.checkSemaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// releaseSemaphore releases a slot in the check semaphore.
func (s *Server) releaseSemaphore() {
	<-s.checkSemaphore
}

// RateLimitMiddleware wraps a handler with rate limiting.
func (s *Server) RateLimitMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.Allow() {
			s.logger.Debug("rate limit exceeded", slog.String("path", r.URL.Path))
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// HandleIndex serves the main web UI.
func (s *Server) HandleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)

		return
	}

	content, err := staticFS.ReadFile("index.html")
	if err != nil {
		s.logger.Error("failed to read index.html", slogtool.ErrorAttr(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(content)
}

// HandleVersion returns the service version.
func (s *Server) HandleVersion(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	buf, err := protojson.Marshal(cliversion.Get())
	if err != nil {
		s.logger.Error("failed to marshal version info", slogtool.ErrorAttr(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(buf)
}

// HandleLogLevel handles PUT requests to change log level at runtime.
func (s *Server) HandleLogLevel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPut {
		s.metrics.RequestsTotal.WithLabelValues("/api/log/level", "method_not_allowed").Inc()
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Error: "method not allowed, use PUT"})

		return
	}

	var req struct {
		Level string `json:"level"`
	}

	body := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		s.metrics.RequestsTotal.WithLabelValues("/api/log/level", "bad_request").Inc()
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Error: "invalid JSON body"})

		return
	}

	if req.Level == "" {
		s.metrics.RequestsTotal.WithLabelValues("/api/log/level", "bad_request").Inc()
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Error: "level field is required"})

		return
	}

	// Set level for all loggers (using "*" wildcard)
	if ok := s.logmgr.SetLevel("*", req.Level); !ok {
		s.metrics.RequestsTotal.WithLabelValues("/api/log/level", "bad_request").Inc()
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Error: fmt.Sprintf("invalid log level: %s", req.Level)})

		return
	}

	s.logger.Info("log level changed", slog.String("level", req.Level))
	s.metrics.RequestsTotal.WithLabelValues("/api/log/level", "success").Inc()

	_ = json.NewEncoder(w).Encode(struct {
		Level string `json:"level"`
	}{Level: req.Level})
}

// HandleCheckList returns the list of available checks.
func (s *Server) HandleCheckList(w http.ResponseWriter, _ *http.Request) {
	s.logger.Debug("HandleCheckList: listing available checks")
	w.Header().Set("Content-Type", "application/json")

	response := struct {
		Checks []CheckInfo `json:"checks"`
	}{
		Checks: make([]CheckInfo, 0),
	}

	for name, c := range s.registry.All() {
		response.Checks = append(response.Checks, CheckInfo{
			Name:        name,
			Description: c.Description(),
		})
	}

	s.logger.Debug("HandleCheckList: returning checks", slog.Int("count", len(response.Checks)))
	_ = json.NewEncoder(w).Encode(response)
}

// HandleCheck runs checks and returns JSON response.
// Supports both GET (query parameters) and POST (JSON body) requests.
func (s *Server) HandleCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !s.ensureCheckMethodAllowed(w, r.Method, "/api/check") {
		return
	}

	req, checks, ok := s.prepareCheckRequest(w, r)
	if !ok {
		return
	}

	ctx := contextual.NewCancellable(context.Background(), contextual.WithTimeoutOption(s.getTimeout(req.Timeout)))
	defer ctx.Cancel()
	results := make(chan check.Result, len(checks))

	s.processChecks(ctx, req, checks, results)

	_ = ctx.Wait()
	close(results)

	response := CheckResponse{
		Hostname: req.Hostname,
		Results:  make([]check.Result, 0, len(checks)),
	}

	for result := range results {
		response.Results = append(response.Results, result)

		switch result.Status {
		case check.StatusFail, check.StatusError:
			response.Summary.Failed++
		case check.StatusPass:
			response.Summary.Passed++
		case check.StatusPartial:
			response.Summary.Partial++
		case check.StatusWarn:
			response.Summary.Warned++
		case check.StatusSkipped:
			response.Summary.Skipped++
		}
	}

	s.logger.Debug("HandleCheck: all checks complete",
		slog.String("hostname", req.Hostname),
		slog.Int("passed", response.Summary.Passed),
		slog.Int("failed", response.Summary.Failed),
		slog.Int("partial", response.Summary.Partial),
		slog.Int("warned", response.Summary.Warned),
		slog.Int("skipped", response.Summary.Skipped),
	)

	s.metrics.RequestsTotal.WithLabelValues("/api/check", "success").Inc()
	_ = json.NewEncoder(w).Encode(response)
}

func (s *Server) prepareCheckRequest(w http.ResponseWriter, r *http.Request) (*CheckRequest, []check.Check, bool) {
	onError := func(message string) {
		s.writeCheckError(w, message, "bad_request")
	}

	return s.prepareRequest(r, "HandleCheck", onError)
}

func (s *Server) prepareRequest(
	r *http.Request,
	logPrefix string,
	onError func(message string),
) (*CheckRequest, []check.Check, bool) {
	var req *CheckRequest
	{
		var err error
		req, err = s.ParseCheckRequest(r)
		if err != nil {
			s.logger.Debug(logPrefix+": failed to parse request", slog.String("error", err.Error()))
			onError(err.Error())

			return nil, nil, false
		}
	}

	if err := ValidateHostname(req.Hostname); err != nil {
		s.logger.Debug(logPrefix+": invalid hostname parameter",
			slog.String("hostname", req.Hostname), slog.String("error", err.Error()))
		onError(err.Error())

		return nil, nil, false
	}

	{
		var err error
		req.Hostname, err = ParseHostname(req.Hostname)
		if err != nil {
			s.logger.Debug(logPrefix+": failed to normalize hostname",
				slog.String("hostname", req.Hostname), slog.String("error", err.Error()))
			onError(err.Error())

			return nil, nil, false
		}
	}

	checks := s.registry.Filter(req.Checks)

	s.logger.Debug(logPrefix+": processing request",
		slog.String("hostname", req.Hostname), slog.Any("requested_checks", req.Checks),
		slog.Int("available_checks", len(checks)))

	if len(checks) == 0 {
		s.logger.Debug(logPrefix+": no valid checks available",
			slog.String("hostname", req.Hostname), slog.Any("requested", req.Checks))
		onError("no valid checks specified or available")

		return nil, nil, false
	}

	return req, checks, true
}

func (s *Server) writeCheckError(w http.ResponseWriter, message, metricLabel string) {
	s.metrics.RequestsTotal.WithLabelValues("/api/check", metricLabel).Inc()
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: message})
}

func (s *Server) ensureCheckMethodAllowed(w http.ResponseWriter, method, endpoint string) bool {
	if method == http.MethodGet || method == http.MethodPost {
		return true
	}

	w.Header().Set("Content-Type", "application/json")
	s.metrics.RequestsTotal.WithLabelValues(endpoint, "method_not_allowed").Inc()
	w.WriteHeader(http.StatusMethodNotAllowed)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: "method not allowed, use GET or POST"})

	return false
}

// HandleCheckSSE runs checks with Server-Sent Events streaming.
// Supports both GET (query parameters) and POST (JSON body) requests.
func (s *Server) HandleCheckSSE(w http.ResponseWriter, r *http.Request) {
	if !s.ensureCheckMethodAllowed(w, r.Method, "/api/check/sse") {
		return
	}

	var req *CheckRequest
	var checks []check.Check
	{
		var ok bool
		req, checks, ok = s.prepareSSERequest(w, r)
		if !ok {
			return
		}
	}

	var flusher http.Flusher
	{
		var ok bool
		flusher, ok = s.setupSSEStream(w)
		if !ok {
			s.logger.Debug("HandleCheckSSE: streaming not supported")
			s.writeSSEError(w, http.StatusInternalServerError, "streaming not supported", "error")

			return
		}
	}

	ctx := contextual.NewCancellable(r.Context(), contextual.WithTimeoutOption(s.getTimeout(req.Timeout)))
	defer ctx.Cancel()

	// Send initial event
	s.logger.Debug("HandleCheckSSE: sending started event", slog.String("hostname", req.Hostname))
	sendEvent(w, flusher, "started", map[string]string{"hostname": req.Hostname})

	// eg, egCtx := errgroup.WithContext(ctx)
	resultCh := make(chan check.Result, len(checks))

	// Start keepalive goroutine to prevent proxy timeouts
	keepaliveDone := keepAlive(ctx, w, flusher)

	s.processChecks(ctx, req, checks, resultCh)

	go func() {
		_ = ctx.Wait()
		close(resultCh)
		close(keepaliveDone)
	}()

	processed := 0
processResults:
	for {
		select {
		case <-ctx.Done():
			s.logger.Debug("HandleCheckSSE: context cancelled",
				slog.String("hostname", req.Hostname), slog.String("error", ctx.Err().Error()),
			)
			sendEvent(w, flusher, "error", map[string]string{"error": ctx.Err().Error()})
			s.metrics.RequestsTotal.WithLabelValues("/api/check/sse", "error").Inc()

			return
		case result, ok := <-resultCh:
			if !ok {
				break processResults
			}
			processed++

			s.logger.Debug("HandleCheckSSE: sending result event",
				slog.String("hostname", req.Hostname), slog.String("check", result.Name),
				slog.String("status", string(result.Status)), slog.Int("processed", processed),
				slog.Int("total", len(checks)),
			)
			sendEvent(w, flusher, "result", result)
		}
	}

	// Send completion event
	s.logger.Debug(
		"HandleCheckSSE: sending completed event",
		slog.String("hostname", req.Hostname),
		slog.Int("total", processed),
	)
	sendEvent(w, flusher, "completed", map[string]int{
		"total": processed,
	})
	s.metrics.RequestsTotal.WithLabelValues("/api/check/sse", "success").Inc()
}

func (s *Server) prepareSSERequest(w http.ResponseWriter, r *http.Request) (*CheckRequest, []check.Check, bool) {
	onError := func(message string) {
		s.writeSSEError(w, http.StatusBadRequest, message, "bad_request")
	}

	return s.prepareRequest(r, "HandleCheckSSE", onError)
}

func (s *Server) setupSSEStream(w http.ResponseWriter) (http.Flusher, bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)

	return flusher, ok
}

func (s *Server) writeSSEError(w http.ResponseWriter, status int, message, metricLabel string) {
	w.Header().Set("Content-Type", "application/json")
	s.metrics.RequestsTotal.WithLabelValues("/api/check/sse", metricLabel).Inc()
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: message})
}

func keepAlive(ctx contextual.Context, w http.ResponseWriter, flusher http.Flusher) chan any {
	// Start keepalive goroutine to prevent proxy timeouts
	keepaliveDone := make(chan any)
	go func() {
		ticker := time.NewTicker(keepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Send SSE comment as keepalive (ignored by clients but keeps connection alive)
				_, _ = w.Write([]byte(": keepalive\n\n"))
				flusher.Flush()
			case <-ctx.Done():
				return
			case <-keepaliveDone:
				return
			}
		}
	}()
	return keepaliveDone
}

func sendEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, data any) {
	event := map[string]any{
		"type": eventType,
		"data": data,
	}

	payload, _ := json.Marshal(event)
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(payload)
	_, _ = w.Write([]byte("\n\n"))
	flusher.Flush()
}

func (s *Server) processChecks(
	ctx contextual.Context,
	req *CheckRequest,
	checks []check.Check,
	resultCh chan<- check.Result,
) {
	s.logger.Debug("processChecks: started")

	// Group checks by plugin type
	byType := make(map[check.PluginType][]check.Check)
	for _, c := range checks {
		t := c.Info().Type
		byType[t] = append(byType[t], c)
	}

	// Run all stages inside a single ctx.Go so ctx.Wait() in callers still works.
	// Stages execute sequentially in PluginTypeOrder; checks within each stage run in parallel.
	ctx.Go(func() error {
		defer s.logger.Debug("processChecks: completed")

		var accumulated []check.Result

		for _, pluginType := range check.PluginTypeOrder {
			stageChecks := byType[pluginType]
			if len(stageChecks) == 0 {
				continue
			}

			// Snapshot accumulated results for this stage so all checks in the
			// stage see the same input regardless of execution order.
			data := make([]check.Result, len(accumulated))
			copy(data, accumulated)

			stageCh := make(chan check.Result, len(stageChecks))
			eg, _ := errgroup.WithContext(ctx)

			for _, c := range stageChecks {
				s.logger.Debug("processChecks: starting check",
					slog.String("hostname", req.Hostname),
					slog.String("check", c.Name()),
					slog.String("type", string(pluginType)),
				)
				eg.Go(func() error {
					if err := s.acquireSemaphore(ctx); err != nil {
						s.logger.Debug(
							"processChecks: check skipped due context cancellation while waiting for semaphore",
							slog.String("hostname", req.Hostname),
							slog.String("check", c.Name()),
							slog.String("error", err.Error()),
						)
						return nil
					}
					defer s.releaseSemaphore()

					s.logger.Debug(
						"processChecks: running check",
						slog.String("hostname", req.Hostname),
						slog.String("check", c.Name()),
					)
					start := time.Now()
					cfg := s.registry.GetConfig(c.Name())
					result := c.Run(ctx, req.Hostname, cfg, data)
					result.Duration = time.Since(start).Round(time.Millisecond).String()

					// Record metrics
					s.metrics.CheckDuration.WithLabelValues(c.Name()).Observe(time.Since(start).Seconds())
					s.metrics.ChecksTotal.WithLabelValues(c.Name(), string(result.Status)).Inc()

					s.logger.Debug("processChecks: check complete",
						slog.String("hostname", req.Hostname),
						slog.String("check", c.Name()),
						slog.String("status", string(result.Status)),
						slog.String("duration", result.Duration),
					)
					stageCh <- result
					return nil
				})
			}

			_ = eg.Wait()
			close(stageCh)

			for result := range stageCh {
				accumulated = append(accumulated, result)
				resultCh <- result
			}
		}

		return nil
	})
}

type CheckInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type CheckResponse struct {
	Hostname string          `json:"hostname"`
	Results  []check.Result  `json:"results"`
	Summary  SummaryResponse `json:"summary"`
}

type SummaryResponse struct {
	Passed  int `json:"passed"`
	Partial int `json:"partial"`
	Failed  int `json:"failed"`
	Warned  int `json:"warned"`
	Skipped int `json:"skipped"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
