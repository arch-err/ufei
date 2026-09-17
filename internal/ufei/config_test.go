package ufei

import (
	"strings"
	"testing"
)

func TestConfig(t *testing.T) {
	t.Setenv("UFEI_PROBES", `[{"name":"web","protocol":"https","domain":"example.test","ip":"192.0.2.1","port":8443,"path":"/health?q=1","timeout":"150ms"}]`)
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Probes[0].URL != "https://example.test:8443/health?q=1" || c.RecoveryEnabled || c.MetricsPrefix != "ufei" {
		t.Fatalf("unexpected config: %+v", c)
	}
	t.Run("metrics prefix", func(t *testing.T) {
		t.Setenv("UFEI_METRICS_PREFIX", "capcaasoperator_ufei")
		if c, err := LoadConfig(); err != nil || c.MetricsPrefix != "capcaasoperator_ufei" {
			t.Fatalf("valid prefix rejected: %q, %v", c.MetricsPrefix, err)
		}
		for _, prefix := range []string{"", "1ufei", "ufei-bad", strings.Repeat("a", 64)} {
			t.Run(prefix, func(t *testing.T) {
				t.Setenv("UFEI_METRICS_PREFIX", prefix)
				if _, err := LoadConfig(); err == nil {
					t.Fatal("accepted invalid metric prefix")
				}
			})
		}
	})
	for _, key := range []string{"UFEI_TIMEOUT", "UFEI_INTERVAL", "UFEI_COOLDOWN", "UFEI_API_TIMEOUT"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "0s")
			if _, err := LoadConfig(); err == nil {
				t.Fatal("accepted zero duration")
			}
		})
	}
	t.Run("recovery requires allowlist", func(t *testing.T) {
		t.Setenv("UFEI_RECOVERY_ENABLED", "true")
		if _, err := LoadConfig(); err == nil {
			t.Fatal("accepted unscoped recovery")
		}
	})
	for _, raw := range []string{`[]`, `[{"name":"bad","protocol":"udp"}]`, `[{"name":"bad","protocol":"http","url":"http://a","typo":1}]`, `[{"name":"bad","protocol":"http","url":"http://a:99999"}]`, `[{"name":"bad","protocol":"icmp","ip":"127.0.0.1","port":80}]`, `[] []`} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("UFEI_PROBES", raw)
			if _, err := LoadConfig(); err == nil {
				t.Fatal("accepted invalid probe")
			}
		})
	}
	t.Run("operational minimums", func(t *testing.T) {
		for key, value := range map[string]string{
			"UFEI_INTERVAL": "999ms", "UFEI_TIMEOUT": "9ms", "UFEI_API_TIMEOUT": "99ms",
		} {
			t.Run(key, func(t *testing.T) {
				t.Setenv(key, value)
				if _, err := LoadConfig(); err == nil {
					t.Fatalf("accepted unsafe %s", value)
				}
			})
		}
	})
	t.Run("recovery guardrails", func(t *testing.T) {
		t.Setenv("UFEI_RECOVERY_ENABLED", "true")
		t.Setenv("UFEI_EGRESSIP_NAMES", "team-egress")
		t.Setenv("UFEI_NAMESPACE", "team")
		t.Setenv("UFEI_POD_NAME", "ufei")
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "at least two probes") {
			t.Fatalf("single-probe recovery was not rejected: %v", err)
		}
		t.Setenv("UFEI_PROBES", `[{"name":"a","protocol":"tcp","ip":"192.0.2.1","port":80},{"name":"b","protocol":"tcp","ip":"192.0.2.2","port":80}]`)
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "MIN_TIMEOUTS") {
			t.Fatalf("single-timeout recovery was not rejected: %v", err)
		}
		t.Setenv("UFEI_MIN_TIMEOUTS", "2")
		t.Setenv("UFEI_COOLDOWN", "29s")
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "COOLDOWN") {
			t.Fatalf("short recovery cooldown was not rejected: %v", err)
		}
		t.Setenv("UFEI_COOLDOWN", "30s")
		if _, err := LoadConfig(); err != nil {
			t.Fatalf("safe recovery configuration rejected: %v", err)
		}
	})
}
