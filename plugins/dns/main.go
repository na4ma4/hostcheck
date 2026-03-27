// Package main provides the DNS check plugin for hostcheck.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/na4ma4/hostcheck/pkg/check"
)

// rootServers contains the IP addresses of root nameservers (populated from embedded named.root).
var rootServers []string

// DNS implements the check.Check interface for DNS validation.
type DNS struct {
	logger *slog.Logger
}

// NewDNS creates a new DNS check instance.
func NewDNS() *DNS {
	return &DNS{
		logger: slog.Default(),
	}
}

// traceLevel is a custom log level for very detailed DNS query tracing.
// It's lower than Debug (slog.LevelDebug = -4) to allow filtering out verbose logs.
const traceLevel = slog.Level(-5)

// trace logs at trace level for very detailed DNS query information.
func (d *DNS) trace(msg string, args ...any) {
	d.logger.Log(context.Background(), traceLevel, msg, args...)
}

// Name returns the unique name of the check.
func (d *DNS) Name() string {
	return "dns"
}

// Description returns a human-readable description.
func (d *DNS) Description() string {
	return "Validates DNS records (SOA, NS) by emulating recursive DNS lookup from root servers"
}

// refusedError indicates that DNS servers returned REFUSED for a domain query.
// This typically means the servers are not authoritative for the domain or are misconfigured.
type refusedError struct {
	domain  string
	servers []string
	message string
}

func (e *refusedError) Error() string {
	return e.message
}

// noDelegationError indicates that no delegation exists for a domain.
// This is a legitimate case where the domain does not exist in DNS (NXDOMAIN or empty response).
type noDelegationError struct {
	domain  string
	message string
}

func (e *noDelegationError) Error() string {
	return e.message
}

// nameserverTimeoutError indicates that all nameservers failed to respond (timeout/connection errors).
type nameserverTimeoutError struct {
	domain  string
	servers []string
	message string
}

func (e *nameserverTimeoutError) Error() string {
	return e.message
}

// bogonTLDError indicates that the hostname uses a bogon/private TLD that should not be processed.
type bogonTLDError struct {
	hostname string
	tld      string
	message  string
}

func (e *bogonTLDError) Error() string {
	return e.message
}

// bogonTLDs contains TLDs that are reserved for private use or special purposes
// and should not be processed by public DNS infrastructure.
// Based on RFC 2606, RFC 6761, RFC 6762, and other standards.
var bogonTLDs = map[string]bool{
	".local":        true, // RFC 6762 - mDNS
	".localhost":    true, // RFC 6761 - reserved for loopback
	".internal":     true, // Private use
	".home":         true, // Private use (common home networks)
	".corp":         true, // Private corporate networks
	".lan":          true, // Private LAN networks
	".localdomain":  true, // Private use
	".test":         true, // RFC 2606 - reserved for testing
	".example":      true, // RFC 2606 - reserved for documentation
	".invalid":      true, // RFC 2606 - reserved for invalid domains
	".onion":        true, // RFC 7686 - Tor hidden services
}

// isBogonTLD checks if the hostname uses a bogon/private TLD.
// Returns true if the TLD is in the bogon list or if the TLD is purely numeric.
func isBogonTLD(hostname string) (bool, string) {
	hostname = strings.ToLower(strings.TrimSuffix(hostname, "."))
	labels := strings.Split(hostname, ".")
	if len(labels) == 0 {
		return false, ""
	}

	tld := labels[len(labels)-1]

	// Check if TLD is in bogon list
	if bogonTLDs["."+tld] {
		return true, tld
	}

	// Check if TLD is purely numeric (not a valid public TLD)
	allNumeric := true
	for _, r := range tld {
		if r < '0' || r > '9' {
			allNumeric = false
			break
		}
	}
	if allNumeric && len(tld) > 0 {
		return true, tld
	}

	return false, ""
}

// defaultTimeout is the default DNS query timeout (30 seconds).
const defaultTimeout = 30 * time.Second

// Run executes the DNS check against the given hostname.
func (d *DNS) Run(ctx context.Context, hostname string, cfg map[string]any) check.Result {
	start := time.Now()
	details := make([]string, 0)
	tasks := make([]check.ResultTask, 0)

	d.logger.Debug("starting DNS check", "hostname", hostname)

	// Parse timeout from config (default 30 seconds)
	timeout := defaultTimeout
	if cfg != nil {
		if timeoutVal, ok := cfg["timeout"]; ok {
			switch v := timeoutVal.(type) {
			case int:
				timeout = time.Duration(v) * time.Second
			case int64:
				timeout = time.Duration(v) * time.Second
			case float64:
				timeout = time.Duration(v) * time.Second
			case string:
				if dur, err := time.ParseDuration(v); err == nil {
					timeout = dur
				}
			case time.Duration:
				timeout = v
			}
		}
	}
	d.logger.Debug("DNS check timeout configured", "timeout", timeout)

	// Normalize the hostname
	originalHostname := hostname
	hostname = normalizeHostname(hostname)
	if hostname == "" {
		d.logger.Debug("invalid hostname after normalization", "original", originalHostname)
		return check.Result{
			Name:     d.Name(),
			Status:   check.StatusError,
			Message:  "invalid hostname provided",
			Duration: time.Since(start).Round(time.Millisecond).String(),
		}
	}
	d.logger.Debug("normalized hostname", "original", originalHostname, "normalized", hostname)

	// Check for bogon/private TLDs (RFC 2606, RFC 6761, RFC 6762, etc.)
	if isBogon, tld := isBogonTLD(hostname); isBogon {
		d.logger.Debug("hostname uses bogon/private TLD", "hostname", hostname, "tld", tld)
		return check.Result{
			Name:     d.Name(),
			Status:   check.StatusError,
			Message:  fmt.Sprintf("hostname uses bogon/private TLD '.%s' which is not valid for public DNS", tld),
			Details:  []string{fmt.Sprintf("TLD '.%s' is reserved for private use or special purposes", tld)},
			Duration: time.Since(start).Round(time.Millisecond).String(),
		}
	}

	// Perform recursive DNS lookup to find authoritative zone and nameservers
	d.logger.Debug("starting recursive lookup", "hostname", hostname)
	authNS, authZone, delegationChain, lookupErr := d.recursiveLookup(ctx, hostname, timeout)
	details = append(details, delegationChain...)

	// Add recursive lookup task
	if lookupErr != nil {
		d.logger.Debug("recursive lookup failed", "hostname", hostname, "error", lookupErr)

		// Check if it's a REFUSED error for clearer messaging
		var refusedErr *refusedError
		if errors.As(lookupErr, &refusedErr) {
			tasks = append(tasks, check.ResultTask{
				CheckName: "Recursive Lookup",
				Status:    check.StatusFail,
				Message:   fmt.Sprintf("Authoritative nameservers for %s refused query (REFUSED) - servers may no longer be authoritative for this domain", refusedErr.domain),
			})
			details = append(details, fmt.Sprintf("REFUSED by servers: %v", refusedErr.servers))
			return check.Result{
				Name:     d.Name(),
				Status:   check.StatusFail,
				Message:  fmt.Sprintf("DNS lookup failed: authoritative nameservers for %s refused the query", refusedErr.domain),
				Details:  details,
				Tasks:    tasks,
				Duration: time.Since(start).Round(time.Millisecond).String(),
			}
		}

		tasks = append(tasks, check.ResultTask{
			CheckName: "Recursive Lookup",
			Status:    check.StatusFail,
			Message:   lookupErr.Error(),
		})
		return check.Result{
			Name:     d.Name(),
			Status:   check.StatusFail,
			Message:  fmt.Sprintf("DNS lookup failed for %s: %v", hostname, lookupErr),
			Details:  details,
			Tasks:    tasks,
			Duration: time.Since(start).Round(time.Millisecond).String(),
		}
	}

	tasks = append(tasks, check.ResultTask{
		CheckName: "Recursive Lookup",
		Status:    check.StatusPass,
		Message:   fmt.Sprintf("Found authoritative zone: %s", authZone),
	})
	d.logger.Debug("recursive lookup complete", "hostname", hostname, "authoritative_zone", authZone, "authoritative_nameservers", authNS)

	// Check if hostname is a CNAME by querying A records
	cnameTarget, cnameDetails := d.queryCNAME(ctx, hostname, authNS, timeout)
	details = append(details, cnameDetails...)

	if cnameTarget != "" {
		tasks = append(tasks, check.ResultTask{
			CheckName: "CNAME Check",
			Status:    check.StatusPass,
			Message:   fmt.Sprintf("%s is a CNAME pointing to %s", hostname, cnameTarget),
		})

		// For CNAMEs, we've already verified the parent zone has proper delegation
		// The check passes since CNAME is valid DNS configuration
		return check.Result{
			Name:     d.Name(),
			Status:   check.StatusPass,
			Message:  fmt.Sprintf("%s is a valid CNAME pointing to %s", hostname, cnameTarget),
			Details:  details,
			Tasks:    tasks,
			Duration: time.Since(start).Round(time.Millisecond).String(),
		}
	}

	// Query SOA from authoritative nameservers (at the zone apex, not the full hostname)
	soaFound, soaDetails, soaRecord := d.querySOAFromAuthoritative(ctx, authZone, authNS, timeout)
	details = append(details, soaDetails...)

	if soaFound {
		tasks = append(tasks, check.ResultTask{
			CheckName: "SOA Record",
			Status:    check.StatusPass,
			Message:   fmt.Sprintf("Serial: %d, Admin: %s", soaRecord.Serial, soaRecord.Mbox),
		})
	} else {
		tasks = append(tasks, check.ResultTask{
			CheckName: "SOA Record",
			Status:    check.StatusFail,
			Message:   fmt.Sprintf("No SOA record found at %s", authZone),
		})
	}

	// Query NS from authoritative nameservers (at the zone apex)
	nsFound, nsDetails, nsRecords := d.queryNSFromAuthoritative(ctx, authZone, authNS, timeout)
	details = append(details, nsDetails...)

	if nsFound {
		tasks = append(tasks, check.ResultTask{
			CheckName: "NS Records",
			Status:    check.StatusPass,
			Message:   fmt.Sprintf("Found %d nameserver(s): %s", len(nsRecords), strings.Join(nsRecords, ", ")),
		})
	} else {
		tasks = append(tasks, check.ResultTask{
			CheckName: "NS Records",
			Status:    check.StatusFail,
			Message:   fmt.Sprintf("No NS records found at %s", authZone),
		})
	}

	// Determine overall status based on tasks
	status, message := determineStatusFromTasks(tasks, hostname)
	d.logger.Debug("DNS check complete", "hostname", hostname, "status", status, "message", message)

	return check.Result{
		Name:     d.Name(),
		Status:   status,
		Message:  message,
		Details:  details,
		Tasks:    tasks,
		Duration: time.Since(start).Round(time.Millisecond).String(),
	}
}

// delegation represents a delegation point in the DNS hierarchy.
type delegation struct {
	domain string
	ns     []string            // nameserver hostnames
	nsIP   map[string][]string // nameserver hostname -> IP addresses (glue records)
}

// authoritativeZone represents an authoritative zone (no further delegation).
type authoritativeZone struct {
	zone string   // the actual zone name (from SOA record)
	soa  *dns.SOA // SOA record if found in authority section
}

// queryDelegationResult is the result of a delegation query.
type queryDelegationResult struct {
	delegation       *delegation       // non-nil if delegation found
	authoritative    *authoritativeZone // non-nil if at authoritative zone
}

// recursiveLookup emulates a recursive DNS lookup starting from root servers.
// Returns: authoritative nameservers, authoritative zone name, details, error
func (d *DNS) recursiveLookup(ctx context.Context, hostname string, timeout time.Duration) ([]string, string, []string, error) {
	details := make([]string, 0)

	// Split hostname into labels (e.g., "www.example.com" -> ["www", "example", "com"])
	labels := splitDomain(hostname)
	if len(labels) == 0 {
		return nil, "", details, fmt.Errorf("invalid hostname")
	}

	d.trace("recursiveLookup: starting", "hostname", hostname, "labels", labels)

	// Start with root servers
	currentNS := rootServers
	currentDomain := ""

	// Walk down the DNS hierarchy from TLD to the full domain
	// Start from the rightmost label (TLD) and work left
	for i := len(labels) - 1; i >= 0; i-- {
		select {
		case <-ctx.Done():
			return nil, "", details, ctx.Err()
		default:
		}

		if currentDomain == "" {
			currentDomain = labels[i]
		} else {
			currentDomain = labels[i] + "." + currentDomain
		}

		d.trace("recursiveLookup: querying level", "hostname", hostname, "level_domain", currentDomain, "current_ns", currentNS)

		// Query for NS records at this level
		result, err := d.queryDelegation(ctx, currentDomain, currentNS, timeout)
		if err != nil {
			d.logger.Debug("recursiveLookup: delegation query failed", "hostname", hostname, "domain", currentDomain, "error", err)
			details = append(details, fmt.Sprintf("Delegation query for %s failed: %v", currentDomain, err))
			return nil, "", details, err
		}

		// Check if we've reached an authoritative zone
		if result.authoritative != nil {
			zone := result.authoritative.zone
			d.trace("recursiveLookup: at authoritative zone", "hostname", hostname, "queried_domain", currentDomain, "actual_zone", zone)
			details = append(details, fmt.Sprintf("Authoritative zone reached: %s", zone))
			return currentNS, zone, details, nil
		}

		// We have a delegation
		if result.delegation != nil {
			d.trace("recursiveLookup: delegation found", "hostname", hostname, "domain", currentDomain, "ns", result.delegation.ns)
			details = append(details, fmt.Sprintf("Delegation for %s: %v", currentDomain, result.delegation.ns))

			// Use the delegated nameservers for the next level
			// Prefer glue records if available, otherwise resolve nameservers
			currentNS = d.getNSAddresses(ctx, result.delegation)
		}
	}

	d.trace("recursiveLookup: completed with authoritative NS", "hostname", hostname, "nameservers", currentNS)
	return currentNS, currentDomain, details, nil
}

// queryDelegation queries nameservers for NS records and returns delegation info.
// Returns typed errors for different failure scenarios:
// - refusedError: all servers returned REFUSED
// - nameserverTimeoutError: all servers failed to respond (timeout/connection errors)
// - noDelegationError: legitimate NXDOMAIN or empty response (no delegation exists)
func (d *DNS) queryDelegation(ctx context.Context, domain string, nameservers []string, timeout time.Duration) (*queryDelegationResult, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), dns.TypeNS)

	var lastRcode int
	var lastServer string
	refusedServers := make([]string, 0)
	failedServers := make([]string, 0) // servers that failed to respond

	for _, server := range nameservers {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		d.trace("queryDelegation: querying server", "domain", domain, "server", server)

		c := &dns.Client{Timeout: timeout}
		r, _, err := c.ExchangeContext(ctx, m, net.JoinHostPort(server, "53"))
		if err != nil {
			d.trace("queryDelegation: query failed", "domain", domain, "server", server, "error", err)
			lastServer = server
			failedServers = append(failedServers, server)
			continue
		}

		d.trace("queryDelegation: received response", "domain", domain, "server", server,
			"rcode", dns.RcodeToString[r.Rcode],
			"answers", len(r.Answer),
			"authority", len(r.Ns),
			"additional", len(r.Extra))

		lastRcode = r.Rcode
		lastServer = server

		// Track REFUSED responses specifically
		if r.Rcode == dns.RcodeRefused {
			d.trace("queryDelegation: server refused query", "domain", domain, "server", server)
			refusedServers = append(refusedServers, server)
			continue
		}

		if r.Rcode != dns.RcodeSuccess {
			d.trace("queryDelegation: non-success rcode", "domain", domain, "server", server, "rcode", dns.RcodeToString[r.Rcode])
			continue
		}

		// Check for NS records in answer section (authoritative response)
		nsRecords := make([]string, 0)
		for _, ans := range r.Answer {
			if ns, ok := ans.(*dns.NS); ok {
				// Check if this NS record is actually for the domain we queried
				// If it's for a parent domain, this hostname is not a delegated zone
				nsDomain := strings.TrimSuffix(ns.Hdr.Name, ".")
				if nsDomain != domain {
					d.trace("queryDelegation: NS record is for parent domain, not delegation",
						"queried_domain", domain, "ns_domain", nsDomain)
					// This hostname is a leaf (A/CNAME), not a zone
					// Return the parent zone as authoritative
					parentZone := getParentDomain(domain)
					return &queryDelegationResult{
						authoritative: &authoritativeZone{
							zone: parentZone,
						},
					}, nil
				}
				nsRecords = append(nsRecords, ns.Ns)
			}
		}

		if len(nsRecords) > 0 {
			d.trace("queryDelegation: found NS in answer section", "domain", domain, "ns_records", nsRecords)

			// Extract glue records from additional section
			glueRecords := make(map[string][]string)
			for _, extra := range r.Extra {
				if a, ok := extra.(*dns.A); ok {
					// Remove trailing dot from hostname if present
					hostname := strings.TrimSuffix(a.Hdr.Name, ".")
					glueRecords[hostname] = append(glueRecords[hostname], a.A.String())
					d.trace("queryDelegation: found glue A record", "domain", domain, "ns_hostname", hostname, "ip", a.A.String())
				}
			}

			return &queryDelegationResult{
				delegation: &delegation{
					domain: domain,
					ns:     nsRecords,
					nsIP:   glueRecords,
				},
			}, nil
		}

		// Check for delegation in authority section (referral response)
		authNS := make([]string, 0)
		glueRecords := make(map[string][]string)
		var authoritySOA *dns.SOA

		for _, auth := range r.Ns {
			if ns, ok := auth.(*dns.NS); ok {
				authNS = append(authNS, ns.Ns)
			}
			if soa, ok := auth.(*dns.SOA); ok {
				// SOA in authority means we're at the authoritative zone (no delegation)
				// The SOA record tells us the actual zone name
				authoritySOA = soa
				d.trace("queryDelegation: found SOA in authority", "domain", domain, "soa_zone", soa.Hdr.Name, "soa_mbox", soa.Mbox)
			}
		}

		// If we found an SOA in authority, this is an authoritative zone (no further delegation)
		if authoritySOA != nil {
			zoneName := strings.TrimSuffix(authoritySOA.Hdr.Name, ".")
			d.trace("queryDelegation: at authoritative zone", "domain", domain, "zone", zoneName)
			return &queryDelegationResult{
				authoritative: &authoritativeZone{
					zone: zoneName,
					soa:  authoritySOA,
				},
			}, nil
		}

		for _, extra := range r.Extra {
			if a, ok := extra.(*dns.A); ok {
				hostname := strings.TrimSuffix(a.Hdr.Name, ".")
				glueRecords[hostname] = append(glueRecords[hostname], a.A.String())
				d.trace("queryDelegation: found glue A record in referral", "domain", domain, "ns_hostname", hostname, "ip", a.A.String())
			}
		}

		if len(authNS) > 0 {
			d.trace("queryDelegation: found delegation in authority section", "domain", domain, "delegated_ns", authNS)
			return &queryDelegationResult{
				delegation: &delegation{
					domain: domain,
					ns:     authNS,
					nsIP:   glueRecords,
				},
			}, nil
		}
	}

	// If all servers returned REFUSED, provide a clear error message
	if len(refusedServers) > 0 && len(refusedServers) == len(nameservers) {
		return nil, &refusedError{
			domain:    domain,
			servers:   refusedServers,
			message:   fmt.Sprintf("All authoritative nameservers for %s refused the query (REFUSED)", domain),
		}
	}

	// If all servers failed to respond (timeout/connection errors)
	if len(failedServers) > 0 && len(failedServers) == len(nameservers) {
		return nil, &nameserverTimeoutError{
			domain:  domain,
			servers: failedServers,
			message: fmt.Sprintf("All nameservers for %s failed to respond (timeout/connection errors)", domain),
		}
	}

	// If some servers refused, mention it in the error
	if len(refusedServers) > 0 {
		return nil, fmt.Errorf("query refused by %v for %s (rcode from %s: %s)",
			refusedServers, domain, lastServer, dns.RcodeToString[lastRcode])
	}

	// No delegation exists - legitimate NXDOMAIN or empty response
	return nil, &noDelegationError{
		domain:  domain,
		message: fmt.Sprintf("no delegation exists for %s (last response from %s: %s)",
			domain, lastServer, dns.RcodeToString[lastRcode]),
	}
}

// getNSAddresses returns IP addresses for nameservers, using glue records or resolving them.
func (d *DNS) getNSAddresses(ctx context.Context, delegation *delegation) []string {
	var addresses []string

	for _, nsHostname := range delegation.ns {
		// Remove trailing dot
		nsHostname = strings.TrimSuffix(nsHostname, ".")

		// Check if we have glue records
		if ips, ok := delegation.nsIP[nsHostname]; ok && len(ips) > 0 {
			d.trace("getNSAddresses: using glue record", "ns_hostname", nsHostname, "ips", ips)
			addresses = append(addresses, ips...)
			continue
		}

		// No glue record, resolve the nameserver
		d.trace("getNSAddresses: resolving nameserver", "ns_hostname", nsHostname)
		addrs, err := net.LookupHost(nsHostname)
		if err != nil {
			d.trace("getNSAddresses: failed to resolve nameserver", "ns_hostname", nsHostname, "error", err)
			continue
		}
		d.trace("getNSAddresses: resolved nameserver", "ns_hostname", nsHostname, "ips", addrs)
		addresses = append(addresses, addrs...)
	}

	return addresses
}

// querySOAFromAuthoritative queries SOA records from authoritative nameservers.
func (d *DNS) querySOAFromAuthoritative(ctx context.Context, hostname string, nameservers []string, timeout time.Duration) (bool, []string, *dns.SOA) {
	details := make([]string, 0)

	for _, ns := range nameservers {
		select {
		case <-ctx.Done():
			return false, details, nil
		default:
		}

		d.trace("querySOAFromAuthoritative: querying SOA", "hostname", hostname, "nameserver", ns)

		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(hostname), dns.TypeSOA)

		c := &dns.Client{Timeout: timeout}
		r, _, err := c.ExchangeContext(ctx, m, net.JoinHostPort(ns, "53"))
		if err != nil {
			d.trace("querySOAFromAuthoritative: query failed", "hostname", hostname, "nameserver", ns, "error", err)
			continue
		}

		d.trace("querySOAFromAuthoritative: received response", "hostname", hostname, "nameserver", ns,
			"rcode", dns.RcodeToString[r.Rcode], "answers", len(r.Answer))

		if r.Rcode != dns.RcodeSuccess {
			continue
		}

		for _, ans := range r.Answer {
			if soa, ok := ans.(*dns.SOA); ok {
				d.trace("querySOAFromAuthoritative: found SOA", "hostname", hostname, "nameserver", ns, "mbox", soa.Mbox, "serial", soa.Serial)
				details = append(details, fmt.Sprintf("SOA record: %s (serial: %d, admin: %s)", hostname, soa.Serial, soa.Mbox))
				return true, details, soa
			}
		}
	}

	details = append(details, "No SOA record found at authoritative nameservers")
	return false, details, nil
}

// queryNSFromAuthoritative queries NS records from authoritative nameservers.
func (d *DNS) queryNSFromAuthoritative(ctx context.Context, hostname string, nameservers []string, timeout time.Duration) (bool, []string, []string) {
	details := make([]string, 0)

	for _, ns := range nameservers {
		select {
		case <-ctx.Done():
			return false, details, nil
		default:
		}

		d.trace("queryNSFromAuthoritative: querying NS", "hostname", hostname, "nameserver", ns)

		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(hostname), dns.TypeNS)

		c := &dns.Client{Timeout: timeout}
		r, _, err := c.ExchangeContext(ctx, m, net.JoinHostPort(ns, "53"))
		if err != nil {
			d.trace("queryNSFromAuthoritative: query failed", "hostname", hostname, "nameserver", ns, "error", err)
			continue
		}

		d.trace("queryNSFromAuthoritative: received response", "hostname", hostname, "nameserver", ns,
			"rcode", dns.RcodeToString[r.Rcode], "answers", len(r.Answer))

		if r.Rcode != dns.RcodeSuccess {
			continue
		}

		nsRecords := make([]string, 0)
		for _, ans := range r.Answer {
			if nsRecord, ok := ans.(*dns.NS); ok {
				nsRecords = append(nsRecords, nsRecord.Ns)
			}
		}

		if len(nsRecords) > 0 {
			d.trace("queryNSFromAuthoritative: found NS records", "hostname", hostname, "nameserver", ns, "ns_records", nsRecords)
			for _, nsRecord := range nsRecords {
				details = append(details, fmt.Sprintf("NS: %s", nsRecord))
			}
			return true, details, nsRecords
		}
	}

	details = append(details, "No NS records found at authoritative nameservers")
	return false, details, nil
}

// queryCNAME checks if a hostname is a CNAME and returns the target if so.
// Tries ALL nameservers before returning "not a CNAME" - if any nameserver reports a CNAME,
// returns that CNAME target; only returns empty string after trying all servers.
func (d *DNS) queryCNAME(ctx context.Context, hostname string, nameservers []string, timeout time.Duration) (string, []string) {
	details := make([]string, 0)

	// Track if we found CNAME from any server
	var foundCNAMETarget string

	for _, ns := range nameservers {
		select {
		case <-ctx.Done():
			return "", details
		default:
		}

		d.trace("queryCNAME: querying CNAME", "hostname", hostname, "nameserver", ns)

		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(hostname), dns.TypeA)

		c := &dns.Client{Timeout: timeout}
		r, _, err := c.ExchangeContext(ctx, m, net.JoinHostPort(ns, "53"))
		if err != nil {
			d.trace("queryCNAME: query failed", "hostname", hostname, "nameserver", ns, "error", err)
			continue
		}

		d.trace("queryCNAME: received response", "hostname", hostname, "nameserver", ns,
			"rcode", dns.RcodeToString[r.Rcode], "answers", len(r.Answer))

		if r.Rcode != dns.RcodeSuccess {
			continue
		}

		// Check for CNAME in answer section
		for _, ans := range r.Answer {
			if cname, ok := ans.(*dns.CNAME); ok {
				target := strings.TrimSuffix(cname.Target, ".")
				d.trace("queryCNAME: found CNAME", "hostname", hostname, "target", target, "nameserver", ns)
				details = append(details, fmt.Sprintf("CNAME: %s -> %s (from %s)", hostname, target, ns))
				// Record the CNAME but continue checking other servers
				if foundCNAMETarget == "" {
					foundCNAMETarget = target
				}
			}
		}
	}

	// If we found a CNAME from any server, return it
	if foundCNAMETarget != "" {
		return foundCNAMETarget, details
	}

	// No CNAME found from any server
	details = append(details, fmt.Sprintf("No CNAME found for %s (checked all nameservers)", hostname))
	return "", details
}

// determineStatusFromTasks determines the overall check status based on task results.
func determineStatusFromTasks(tasks []check.ResultTask, hostname string) (check.Status, string) {
	if len(tasks) == 0 {
		return check.StatusFail, fmt.Sprintf("No checks performed for %s", hostname)
	}

	failCount := 0
	warnCount := 0
	passCount := 0

	for _, task := range tasks {
		switch task.Status {
		case check.StatusFail, check.StatusError:
			failCount++
		case check.StatusWarn:
			warnCount++
		case check.StatusPass:
			passCount++
		}
	}

	// If ALL tasks fail, return StatusFail
	if failCount > 0 && passCount == 0 && warnCount == 0 {
		return check.StatusFail, fmt.Sprintf("DNS check failed for %s", hostname)
	}

	// If ANY task fails but at least one passes, return StatusPartial
	if failCount > 0 && passCount > 0 {
		return check.StatusPartial, fmt.Sprintf("DNS check partially passed for %s", hostname)
	}

	// If there are warnings (and no failures with passes), return StatusWarn
	if warnCount > 0 {
		return check.StatusWarn, fmt.Sprintf("DNS check passed with warnings for %s", hostname)
	}

	// All tasks passed
	if passCount > 0 {
		return check.StatusPass, fmt.Sprintf("DNS check passed for %s", hostname)
	}

	return check.StatusWarn, fmt.Sprintf("DNS check completed for %s", hostname)
}

// normalizeHostname removes protocol and path from hostname.
func normalizeHostname(hostname string) string {
	// Remove protocol prefix
	if idx := strings.Index(hostname, "://"); idx != -1 {
		hostname = hostname[idx+3:]
	}

	// Remove path
	if idx := strings.Index(hostname, "/"); idx != -1 {
		hostname = hostname[:idx]
	}

	// Remove port
	if idx := strings.Index(hostname, ":"); idx != -1 {
		hostname = hostname[:idx]
	}

	return hostname
}

// splitDomain splits a domain into labels.
func splitDomain(domain string) []string {
	domain = dns.Fqdn(domain)
	if domain == "" || domain == "." {
		return nil
	}
	// Remove trailing dot
	domain = strings.TrimSuffix(domain, ".")
	return strings.Split(domain, ".")
}

// getParentDomain returns the parent domain (removes the leftmost label).
func getParentDomain(domain string) string {
	domain = strings.TrimSuffix(domain, ".")
	labels := strings.Split(domain, ".")
	if len(labels) <= 1 {
		return ""
	}
	return strings.Join(labels[1:], ".")
}

// Check is the exported plugin instance.
var Check check.Check = NewDNS()

func init() {
	// Ensure Check implements the interface
	var _ check.Check = &DNS{}
}
