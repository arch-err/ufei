package ufei

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

type Probe struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	IP       string `json:"ip,omitempty"`
	Port     int    `json:"port,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Path     string `json:"path,omitempty"`
	URL      string `json:"url,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
	duration time.Duration
}

type Config struct {
	Probes                                  []Probe
	EgressIPs                               []string
	Interval, Timeout, Cooldown, APITimeout time.Duration
	FailureThreshold, MinTimeouts           int
	RecoveryEnabled                         bool
	Listen, Namespace, PodName              string
}

const (
	minInterval   = time.Second
	minTimeout    = 10 * time.Millisecond
	minAPITimeout = 100 * time.Millisecond
	minCooldown   = 30 * time.Second
)

func LoadConfig() (Config, error) {
	c := Config{
		Listen:    env("UFEI_LISTEN_ADDRESS", ":8080"),
		Namespace: os.Getenv("UFEI_NAMESPACE"),
		PodName:   os.Getenv("UFEI_POD_NAME"),
	}
	for _, x := range []struct {
		name, fallback string
		dest           *time.Duration
	}{
		{"UFEI_INTERVAL", "10s", &c.Interval}, {"UFEI_TIMEOUT", "2s", &c.Timeout},
		{"UFEI_COOLDOWN", "5m", &c.Cooldown}, {"UFEI_API_TIMEOUT", "5s", &c.APITimeout},
	} {
		v, err := time.ParseDuration(env(x.name, x.fallback))
		minimum := map[string]time.Duration{
			"UFEI_INTERVAL": minInterval, "UFEI_TIMEOUT": minTimeout,
			"UFEI_COOLDOWN": time.Nanosecond, "UFEI_API_TIMEOUT": minAPITimeout,
		}[x.name]
		if err != nil || v < minimum {
			return c, fmt.Errorf("%s must be at least %s", x.name, minimum)
		}
		*x.dest = v
	}
	for _, x := range []struct {
		name, fallback string
		dest           *int
	}{
		{"UFEI_FAILURE_THRESHOLD", "3", &c.FailureThreshold}, {"UFEI_MIN_TIMEOUTS", "1", &c.MinTimeouts},
	} {
		v, err := strconv.Atoi(env(x.name, x.fallback))
		if err != nil || v < 1 {
			return c, fmt.Errorf("%s must be a positive integer", x.name)
		}
		*x.dest = v
	}
	var err error
	c.RecoveryEnabled, err = strconv.ParseBool(env("UFEI_RECOVERY_ENABLED", "false"))
	if err != nil {
		return c, fmt.Errorf("UFEI_RECOVERY_ENABLED: %w", err)
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return c, fmt.Errorf("UFEI_LISTEN_ADDRESS: %w", err)
	}
	seen := map[string]bool{}
	if raw := os.Getenv("UFEI_EGRESSIP_NAMES"); raw != "" {
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if len(validation.IsDNS1123Subdomain(name)) != 0 || seen[name] {
				return c, fmt.Errorf("invalid or duplicate EgressIP name %q", name)
			}
			seen[name] = true
			c.EgressIPs = append(c.EgressIPs, name)
		}
	}
	if c.RecoveryEnabled && len(c.EgressIPs) == 0 {
		return c, fmt.Errorf("recovery requires explicit UFEI_EGRESSIP_NAMES")
	}
	if len(c.EgressIPs) > 0 && (c.Namespace == "" || c.PodName == "") {
		return c, fmt.Errorf("EgressIP validation requires UFEI_NAMESPACE and UFEI_POD_NAME")
	}
	decoder := json.NewDecoder(strings.NewReader(os.Getenv("UFEI_PROBES")))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c.Probes); err != nil {
		return c, fmt.Errorf("UFEI_PROBES must be a JSON array: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("UFEI_PROBES contains trailing data")
	}
	if len(c.Probes) == 0 || len(c.Probes) > 64 {
		return c, fmt.Errorf("configure between 1 and 64 probes")
	}
	if c.MinTimeouts > len(c.Probes) {
		return c, fmt.Errorf("UFEI_MIN_TIMEOUTS exceeds probe count")
	}
	seen = map[string]bool{}
	for i := range c.Probes {
		p := &c.Probes[i]
		if p.Name == "" || seen[p.Name] {
			return c, fmt.Errorf("probe names must be nonempty and unique")
		}
		seen[p.Name] = true
		p.duration = c.Timeout
		if p.Timeout != "" {
			p.duration, err = time.ParseDuration(p.Timeout)
			if err != nil || p.duration < minTimeout {
				return c, fmt.Errorf("probe %s: timeout must be at least %s", p.Name, minTimeout)
			}
		}
		if p.IP != "" && net.ParseIP(p.IP) == nil {
			return c, fmt.Errorf("probe %s: invalid IP", p.Name)
		}
		if p.Port < 0 || p.Port > 65535 {
			return c, fmt.Errorf("probe %s: invalid port", p.Name)
		}
		if p.Domain != "" && (strings.ContainsAny(p.Domain, "/: \t\r\n") || strings.HasPrefix(p.Domain, "-")) {
			return c, fmt.Errorf("probe %s: invalid domain", p.Name)
		}
		switch p.Protocol {
		case "http", "https":
			if p.URL != "" && (p.Domain != "" || p.Path != "" || p.Port != 0) {
				return c, fmt.Errorf("probe %s: URL cannot be combined with domain, path or port; IP override is allowed", p.Name)
			}
			if p.URL == "" {
				host := p.Domain
				if host == "" {
					host = p.IP
				}
				if host == "" {
					return c, fmt.Errorf("probe %s: URL, domain or IP required", p.Name)
				}
				if p.Port != 0 {
					host = net.JoinHostPort(host, strconv.Itoa(p.Port))
				} else if strings.Contains(host, ":") {
					host = "[" + host + "]"
				}
				path := p.Path
				if path == "" {
					path = "/"
				}
				if !strings.HasPrefix(path, "/") {
					return c, fmt.Errorf("probe %s: path must start with /", p.Name)
				}
				p.URL = p.Protocol + "://" + host + path
			}
			u, e := url.Parse(p.URL)
			if e != nil || u.Scheme != p.Protocol || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
				return c, fmt.Errorf("probe %s: invalid URL or protocol mismatch", p.Name)
			}
			if port := u.Port(); port != "" {
				n, e := strconv.Atoi(port)
				if e != nil || n < 1 || n > 65535 {
					return c, fmt.Errorf("probe %s: invalid URL port", p.Name)
				}
			}
		case "tcp", "icmp":
			if p.IP == "" && p.Domain == "" {
				return c, fmt.Errorf("probe %s: IP or domain required", p.Name)
			}
			if p.URL != "" || p.Path != "" {
				return c, fmt.Errorf("probe %s: URL/path only apply to HTTP", p.Name)
			}
			if p.Protocol == "tcp" && p.Port == 0 {
				return c, fmt.Errorf("probe %s: TCP requires port", p.Name)
			}
			if p.Protocol == "icmp" && p.Port != 0 {
				return c, fmt.Errorf("probe %s: ICMP has no port", p.Name)
			}
		default:
			return c, fmt.Errorf("probe %s: protocol must be http, https, tcp or icmp", p.Name)
		}
	}
	if c.RecoveryEnabled {
		if len(c.Probes) < 2 || c.MinTimeouts < 2 {
			return c, fmt.Errorf("recovery requires at least two probes and UFEI_MIN_TIMEOUTS of at least 2")
		}
		if c.Cooldown < minCooldown {
			return c, fmt.Errorf("recovery requires UFEI_COOLDOWN of at least %s", minCooldown)
		}
	}
	return c, nil
}

func env(name, fallback string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return fallback
}
