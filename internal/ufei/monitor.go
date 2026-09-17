package ufei

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	Success, Duration, LastRun, LastSuccess           *prometheus.GaugeVec
	Results, Deletes                                  *prometheus.CounterVec
	Assigned                                          *prometheus.GaugeVec
	Failures, APIReady, RecoveryEnabled, LastRecovery prometheus.Gauge
}

func NewMetrics(reg prometheus.Registerer, prefix string) *Metrics {
	m := &Metrics{}
	gauge := func(name, help string) *prometheus.GaugeVec {
		g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: prefix, Name: name, Help: help}, []string{"probe", "protocol"})
		reg.MustRegister(g)
		return g
	}
	m.Success = gauge("probe_success", "Whether the last completed probe succeeded.")
	m.Duration = gauge("probe_duration_seconds", "Duration of the last completed probe, including DNS and TLS.")
	m.LastRun = gauge("probe_last_run_timestamp_seconds", "Timestamp of the last completed probe.")
	m.LastSuccess = gauge("probe_last_success_timestamp_seconds", "Timestamp of the last successful probe; zero until first success.")
	m.Results = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: prefix, Name: "probe_total", Help: "Completed probes by outcome: success, timeout, error, canceled."}, []string{"probe", "protocol", "outcome"})
	m.Deletes = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: prefix, Name: "egressip_delete_total", Help: "EgressIP delete requests by result: success or error."}, []string{"egressip", "result"})
	m.Assigned = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: prefix, Name: "egressip_assignment_info", Help: "Last validated EgressIP assignments; not a measurement of individual IP reachability."}, []string{"egressip", "ip", "node"})
	single := func(name, help string) prometheus.Gauge {
		g := prometheus.NewGauge(prometheus.GaugeOpts{Namespace: prefix, Name: name, Help: help})
		reg.MustRegister(g)
		return g
	}
	m.Failures = single("consecutive_timeout_rounds", "Consecutive rounds meeting the configured timeout quorum.")
	m.APIReady = single("egressip_validation_success", "Whether all configured EgressIPs could be read, select this pod and are assigned; zero if none configured.")
	m.RecoveryEnabled = single("recovery_enabled", "Whether automatic EgressIP deletion is enabled.")
	m.LastRecovery = single("recovery_last_attempt_timestamp_seconds", "Timestamp of last recovery attempt in this process.")
	reg.MustRegister(m.Results, m.Deletes, m.Assigned)
	return m
}

type Monitor struct {
	Config       Config
	API          EgressAPI
	Metrics      *Metrics
	Probe        func(context.Context, Probe) Result
	failures     int
	generation   string
	nextRecovery time.Time
	outcomes     map[string]string
	apiKnown     bool
	apiValid     bool
}

func NewMonitor(c Config, api EgressAPI, m *Metrics) *Monitor {
	if c.RecoveryEnabled {
		m.RecoveryEnabled.Set(1)
	}
	for _, p := range c.Probes {
		m.Success.WithLabelValues(p.Name, p.Protocol).Set(0)
		m.LastSuccess.WithLabelValues(p.Name, p.Protocol).Set(0)
		for _, outcome := range []string{"success", "timeout", "error", "canceled"} {
			m.Results.WithLabelValues(p.Name, p.Protocol, outcome).Add(0)
		}
	}
	for _, name := range c.EgressIPs {
		for _, result := range []string{"success", "error"} {
			m.Deletes.WithLabelValues(name, result).Add(0)
		}
	}
	// A startup grace period limits repeated deletes across process restarts.
	return &Monitor{Config: c, API: api, Metrics: m, Probe: RunProbe, nextRecovery: time.Now().Add(c.Cooldown), outcomes: make(map[string]string)}
}

func (m *Monitor) Run(ctx context.Context) {
	for ctx.Err() == nil {
		m.Round(ctx)
		// Spread installations out instead of synchronizing probes and API reads.
		timer := time.NewTimer(jitter(m.Config.Interval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func jitter(interval time.Duration) time.Duration {
	window := interval / 10
	if window <= 0 {
		return interval
	}
	return interval + time.Duration(rand.Int64N(int64(window)+1))
}

func (m *Monitor) recordAPIValidation(valid bool, err error) {
	if !valid && (!m.apiKnown || m.apiValid) {
		slog.Warn("EgressIP validation failed; recovery blocked", "error", err)
	} else if valid && m.apiKnown && !m.apiValid {
		slog.Info("EgressIP validation recovered")
	}
	m.apiKnown = true
	m.apiValid = valid
}

func (m *Monitor) snapshot(ctx context.Context) ([]EgressIP, error) {
	apiCtx, cancel := context.WithTimeout(ctx, m.Config.APITimeout)
	defer cancel()
	return m.API.Snapshot(apiCtx)
}

func (m *Monitor) Round(ctx context.Context) {
	var before []EgressIP
	valid := false
	if m.API != nil {
		var err error
		before, err = m.snapshot(ctx)
		m.Metrics.Assigned.Reset()
		if err != nil {
			m.Metrics.APIReady.Set(0)
			m.failures = 0
			m.generation = ""
			m.recordAPIValidation(false, err)
		} else {
			valid = true
			m.Metrics.APIReady.Set(1)
			m.recordAPIValidation(true, nil)
			for _, e := range before {
				for _, item := range e.Status.Items {
					m.Metrics.Assigned.WithLabelValues(e.Name, item.EgressIP, item.Node).Set(1)
				}
			}
			current := fingerprint(before)
			if current != m.generation {
				m.failures = 0
				m.generation = current
			}
		}
	}
	results := make([]Result, len(m.Config.Probes))
	var wg sync.WaitGroup
	for i, p := range m.Config.Probes {
		wg.Go(func() { results[i] = m.Probe(ctx, p) })
	}
	wg.Wait()
	timeouts := 0
	for i, r := range results {
		p := m.Config.Probes[i]
		success := 0.0
		if r.Outcome == "success" {
			success = 1
			m.Metrics.LastSuccess.WithLabelValues(p.Name, p.Protocol).SetToCurrentTime()
		}
		m.Metrics.Success.WithLabelValues(p.Name, p.Protocol).Set(success)
		m.Metrics.Duration.WithLabelValues(p.Name, p.Protocol).Set(r.Duration.Seconds())
		m.Metrics.LastRun.WithLabelValues(p.Name, p.Protocol).SetToCurrentTime()
		m.Metrics.Results.WithLabelValues(p.Name, p.Protocol, r.Outcome).Inc()
		if r.Outcome == "timeout" {
			timeouts++
		}
		previous := m.outcomes[p.Name]
		if r.Err != nil && r.Outcome != "canceled" && r.Outcome != previous {
			slog.Warn("probe state changed", "probe", p.Name, "outcome", r.Outcome, "duration_seconds", r.Duration.Seconds(), "error", r.Err)
		} else if r.Outcome == "success" && previous != "" && previous != "success" {
			slog.Info("probe recovered", "probe", p.Name, "previous_outcome", previous, "duration_seconds", r.Duration.Seconds())
		}
		m.outcomes[p.Name] = r.Outcome
	}
	if ctx.Err() != nil {
		return
	}
	if timeouts >= m.Config.MinTimeouts {
		m.failures++
	} else {
		m.failures = 0
	}
	if m.API != nil && !valid {
		m.failures = 0
	}
	m.Metrics.Failures.Set(float64(m.failures))
	if !m.Config.RecoveryEnabled || !valid || m.failures < m.Config.FailureThreshold || time.Now().Before(m.nextRecovery) {
		return
	}
	after, err := m.snapshot(ctx)
	if err != nil || fingerprint(after) != fingerprint(before) {
		m.failures = 0
		m.Metrics.Failures.Set(0)
		if err != nil {
			m.Metrics.APIReady.Set(0)
			m.Metrics.Assigned.Reset()
			m.recordAPIValidation(false, err)
		}
		slog.Warn("EgressIP state changed or could not be revalidated; recovery skipped", "error", err)
		return
	}
	m.nextRecovery = time.Now().Add(m.Config.Cooldown)
	m.Metrics.LastRecovery.SetToCurrentTime()
	m.failures = 0
	m.Metrics.Failures.Set(0)
	// Attempt every allowlisted resource even if one request fails. Each delete is independent.
	for _, e := range after {
		if ctx.Err() != nil {
			return
		}
		apiCtx, cancel := context.WithTimeout(ctx, m.Config.APITimeout)
		err := m.API.Delete(apiCtx, e)
		cancel()
		result := "success"
		if err != nil {
			result = "error"
		}
		m.Metrics.Deletes.WithLabelValues(e.Name, result).Inc()
		slog.Warn("EgressIP recovery delete", "egressip", e.Name, "uid", e.UID, "result", result, "error", err)
	}
}
