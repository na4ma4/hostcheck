package main

import (
	"bytes"
	_ "embed"
	"log/slog"
	"strings"
)

//go:embed named.root
var rootServersData []byte

// parseRootServers parses the named.root file and returns IPv4 addresses of root servers.
func parseRootServers(data []byte) []string {
	logger := slog.Default()
	var servers []string
	serverMap := make(map[string]string) // hostname -> IPv4

	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)

		// Skip empty lines and comments
		if len(line) == 0 || line[0] == ';' {
			continue
		}

		fields := strings.Fields(string(line))
		if len(fields) < 4 {
			continue
		}

		// Fields: <name> <TTL> <type> <data>
		// For A records: hostname TTL A ipv4
		hostname := fields[0]
		recordType := fields[2]
		dataField := fields[3]

		if recordType == "A" {
			serverMap[hostname] = dataField
			logger.Debug("parseRootServers: found A record", "hostname", hostname, "ip", dataField)
		}
	}

	// Collect all IPv4 addresses
	for _, ip := range serverMap {
		servers = append(servers, ip)
	}

	logger.Debug("parseRootServers: parsed root servers", "count", len(servers))

	return servers
}

// init parses the embedded named.root file at startup.
func init() {
	servers := parseRootServers(rootServersData)
	if len(servers) > 0 {
		rootServers = servers
	}
}
