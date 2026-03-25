# hostcheck

A plugin-based host checking web service that validates DNS records, checks hostnames, and provides a RESTful API for running various checks.

## Features

- Plugin architecture for extensible checks
- RESTful API with JSON responses
- Server-Sent Events (SSE) for real-time check results
- Prometheus metrics endpoint
- Rate limiting and concurrent check management
- Runtime configurable log levels

## Security Requirement

This service is designed to run behind an authentication and authorization middleware or gateway.

- Do not expose hostcheck endpoints directly to untrusted/public networks.
- Enforce authentication for all API routes (including operational routes such as `/metrics`, `/health`, and `/api/log/level`).
- Prefer deployment behind a reverse proxy/API gateway that provides identity, access control, and request filtering.

## Installation

### Docker

```bash
# Pull from registry (if published)
docker pull ghcr.io/na4ma4/hostcheck:latest

# Or build locally
docker build -t hostcheck .

# Run with default settings
docker run -p 8080:8080 hostcheck

# Run with custom settings
docker run -p 8080:8080 \
  -e LISTEN=:8080 \
  -e PLUGINS_DIR=/plugins \
  -e DEBUG=false \
  -e RATE_LIMIT=20 \
  hostcheck
```

### Binary

```bash
# Build from source
go build -o hostcheck ./cmd/hostcheck

# Build plugins
mkdir -p artifacts/plugins
go build -buildmode=plugin -o artifacts/plugins/dns.so ./plugins/dns

# Run
./hostcheck --listen 127.0.0.1:8080 --plugins ./artifacts/plugins
```

## Configuration

### Command-Line Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--listen` | `-l` | `127.0.0.1:8080` | Listen address |
| `--plugins` | `-p` | `./plugins` | Plugin directory |
| `--debug` | `-d` | `false` | Enable debug logging |
| `--rate-limit` | | `10` | Rate limit in requests per second |
| `--max-concurrent` | | `0` (auto: max(4, NumCPU)) | Max concurrent checks |
| `--max-timeout` | | `300s` | Maximum allowed timeout per request |

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `LISTEN` | `127.0.0.1:8080` | Listen address |
| `PLUGINS_DIR` | `./plugins` | Plugin directory |
| `DEBUG` | `false` | Enable debug logging |
| `RATE_LIMIT` | `10` | Rate limit in requests per second |
| `MAX_CONCURRENT` | `0` (auto) | Max concurrent checks |
| `MAX_TIMEOUT` | `300s` | Maximum allowed timeout per request |

### Configuration File

The service looks for a configuration file in the following locations:
- `./hostcheck.yaml`
- `/etc/hostcheck/hostcheck.yaml`
- `$HOME/.hostcheck/hostcheck.yaml`

Example configuration:

```yaml
server:
  listen: "0.0.0.0:8080"
  rate_limit: 20
  max_concurrent: 8
  max_timeout: "5m"

plugins:
  directory: "./artifacts/plugins"
  
  # Per-plugin configuration
  dns:
    timeout: "60s"

debug: false
```

## API Endpoints

### `GET /api/checks`

List all available checks.

**Response:**

```json
{
  "checks": [
    {
      "name": "dns",
      "description": "Validates DNS records (SOA, NS) by emulating recursive DNS lookup from root servers"
    }
  ]
}
```

**Example:**

```bash
curl http://localhost:8080/api/checks
```

### `POST /api/check`

Run checks against a hostname. Returns JSON response with all results.

**Request Body:**

```json
{
  "hostname": "example.com",
  "checks": ["dns"],
  "timeout": 60
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `hostname` | string | Yes | Hostname to check |
| `checks` | []string | No | List of checks to run (empty = all) |
| `timeout` | int | No | Timeout in seconds (capped at max-timeout) |

**Response:**

```json
{
  "hostname": "example.com",
  "results": [
    {
      "name": "dns",
      "status": "PASS",
      "message": "DNS check passed for example.com",
      "details": [
        "Delegation for com: [...]",
        "SOA record: example.com (serial: 123456789, admin: hostmaster.example.com)"
      ],
      "duration": "234ms",
      "tasks": [
        {
          "check_name": "Recursive Lookup",
          "status": "PASS",
          "message": "Found authoritative zone: example.com"
        },
        {
          "check_name": "SOA Record",
          "status": "PASS",
          "message": "Serial: 123456789, Admin: hostmaster.example.com"
        },
        {
          "check_name": "NS Records",
          "status": "PASS",
          "message": "Found 2 nameserver(s): ns1.example.com, ns2.example.com"
        }
      ]
    }
  ],
  "summary": {
    "passed": 1,
    "failed": 0,
    "warned": 0,
    "skipped": 0
  }
}
```

**Example:**

```bash
# Run all checks
curl -X POST http://localhost:8080/api/check \
  -H "Content-Type: application/json" \
  -d '{"hostname": "example.com"}'

# Run specific checks
curl -X POST http://localhost:8080/api/check \
  -H "Content-Type: application/json" \
  -d '{"hostname": "example.com", "checks": ["dns"]}'

# With custom timeout
curl -X POST http://localhost:8080/api/check \
  -H "Content-Type: application/json" \
  -d '{"hostname": "example.com", "timeout": 120}'
```

### `POST /api/check/sse`

Run checks with Server-Sent Events streaming. Returns real-time results as they complete.

**Request Body:** Same as `POST /api/check`

**SSE Event Types:**

1. **started** - Sent when checks begin
   ```
   data: {"type":"started","data":{"hostname":"example.com"}}
   ```

2. **result** - Sent for each completed check
   ```
   data: {"type":"result","data":{"name":"dns","status":"PASS",...}}
   ```

3. **completed** - Sent when all checks finish
   ```
   data: {"type":"completed","data":{"total":1}}
   ```

4. **error** - Sent if an error occurs
   ```
   data: {"type":"error","data":{"error":"context deadline exceeded"}}
   ```

**Example:**

```bash
curl -N -X POST http://localhost:8080/api/check/sse \
  -H "Content-Type: application/json" \
  -d '{"hostname": "example.com"}'
```

### `GET /health`

Health check endpoint for monitoring and load balancers.

**Response:**

```json
{
  "status": "ok",
  "checks": {
    "webserver": "ok",
    "webserver.Context": "ok",
    "webserver.Listen": "ok"
  }
}
```

**Example:**

```bash
curl http://localhost:8080/health
```

### `GET /metrics`

Prometheus metrics endpoint.

**Available Metrics:**

| Metric | Type | Description |
|--------|------|-------------|
| `hostcheck_requests_total` | Counter | Total HTTP requests by endpoint and status |
| `hostcheck_checks_total` | Counter | Total checks executed by name and status |
| `hostcheck_check_duration_seconds` | Histogram | Duration of check execution in seconds |

**Example:**

```bash
curl http://localhost:8080/metrics
```

### `GET /version`

Returns version information.

**Response:**

```json
{
  "version": "1.0.0",
  "commit": "abc123",
  "date": "2025-03-26T12:00:00Z"
}
```

**Example:**

```bash
curl http://localhost:8080/version
```

### `PUT /api/log/level`

Change the log level at runtime.

**Request Body:**

```json
{
  "level": "debug"
}
```

**Valid Levels:** `debug`, `info`, `warn`, `error`

**Response:**

```json
{
  "level": "debug"
}
```

**Example:**

```bash
curl -X PUT http://localhost:8080/api/log/level \
  -H "Content-Type: application/json" \
  -d '{"level": "debug"}'
```

## Check Status Values

| Status | Description |
|--------|-------------|
| `PASS` | Check passed successfully |
| `FAIL` | Check failed |
| `PARTIAL` | Check partially passed (some tasks passed, some failed) |
| `WARN` | Check passed with warnings |
| `ERROR` | Check encountered an error |
| `SKIPPED` | Check was skipped |

## License

See [LICENSE](LICENSE) for details.
