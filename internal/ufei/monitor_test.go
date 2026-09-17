package ufei

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestMetricNames(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := Config{Probes: []Probe{{Name: "web", Protocol: "http"}}, EgressIPs: []string{"edge"}}
	NewMonitor(c, nil, NewMetrics(reg))
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) == 0 {
		t.Fatal("no metric families registered")
	}
	for _, family := range families {
		if !strings.HasPrefix(family.GetName(), "ufei_") {
			t.Fatalf("metric %q does not use ufei prefix", family.GetName())
		}
	}
}

type fakeAPI struct {
	items                    []EgressIP
	deleted                  []string
	snapshotErr, errorDelete error
	calls                    int
	changeOn                 int
}

func (f *fakeAPI) Snapshot(context.Context) ([]EgressIP, error) {
	f.calls++
	items := append([]EgressIP(nil), f.items...)
	if f.calls == f.changeOn {
		items[0].ResourceVersion = "changed"
	}
	return items, f.snapshotErr
}
func (f *fakeAPI) Delete(_ context.Context, e EgressIP) error {
	f.deleted = append(f.deleted, e.Name)
	return f.errorDelete
}
func newTestMonitor() (*Monitor, *fakeAPI) {
	f := &fakeAPI{}
	for _, name := range []string{"a", "b"} {
		f.items = append(f.items, EgressIP{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name), ResourceVersion: "1"}})
	}
	c := Config{Probes: []Probe{{Name: "web", Protocol: "http"}}, EgressIPs: []string{"a", "b"}, RecoveryEnabled: true, FailureThreshold: 2, MinTimeouts: 1, Cooldown: time.Minute, APITimeout: time.Second}
	m := NewMonitor(c, f, NewMetrics(prometheus.NewRegistry()))
	m.nextRecovery = time.Time{}
	m.Probe = func(context.Context, Probe) Result { return Result{Outcome: "timeout"} }
	return m, f
}

func TestRecoveryGroupAndCooldown(t *testing.T) {
	m, f := newTestMonitor()
	ctx := context.Background()
	m.Round(ctx)
	if len(f.deleted) != 0 {
		t.Fatal("deleted before threshold")
	}
	m.Round(ctx)
	if len(f.deleted) != 2 {
		t.Fatalf("group not deleted: %v", f.deleted)
	}
	for range 4 {
		m.Round(ctx)
	}
	if len(f.deleted) != 2 {
		t.Fatal("cooldown not enforced")
	}
	if testutil.ToFloat64(m.Metrics.Deletes.WithLabelValues("b", "success")) != 1 {
		t.Fatal("missing delete metric")
	}
}

func TestRecoveryFailClosed(t *testing.T) {
	for _, scenario := range []string{"disabled", "errors", "api", "generation", "revalidation", "startup"} {
		t.Run(scenario, func(t *testing.T) {
			m, f := newTestMonitor()
			ctx := context.Background()
			switch scenario {
			case "disabled":
				m.Config.RecoveryEnabled = false
			case "errors":
				m.Probe = func(context.Context, Probe) Result { return Result{Outcome: "error"} }
			case "api":
				f.snapshotErr = errors.New("forbidden")
			case "revalidation":
				f.changeOn = 3
			case "startup":
				m.nextRecovery = time.Now().Add(time.Hour)
			}
			m.Round(ctx)
			if scenario == "generation" {
				f.items[0].UID = "recreated"
			}
			m.Round(ctx)
			if len(f.deleted) != 0 {
				t.Fatalf("unsafe delete in %s", scenario)
			}
		})
	}
}

func TestHealthyRoundResetsThresholdAndDeleteErrorsAreBounded(t *testing.T) {
	m, f := newTestMonitor()
	ctx := context.Background()
	m.Round(ctx)
	m.Probe = func(context.Context, Probe) Result { return Result{Outcome: "success"} }
	m.Round(ctx)
	m.Probe = func(context.Context, Probe) Result { return Result{Outcome: "timeout"} }
	m.Round(ctx)
	if len(f.deleted) != 0 {
		t.Fatal("success did not reset threshold")
	}
	f.errorDelete = errors.New("forbidden")
	m.Round(ctx)
	if len(f.deleted) != 2 {
		t.Fatal("one failed delete prevented remaining group attempt")
	}
	for range 4 {
		m.Round(ctx)
	}
	if len(f.deleted) != 2 {
		t.Fatal("failed delete bypassed cooldown")
	}
}

func TestTimeoutQuorum(t *testing.T) {
	m, f := newTestMonitor()
	m.Config.MinTimeouts = 2
	m.Config.Probes = append(m.Config.Probes, Probe{Name: "other", Protocol: "tcp"})
	m.Probe = func(_ context.Context, p Probe) Result {
		if p.Name == "web" {
			return Result{Outcome: "timeout"}
		}
		return Result{Outcome: "success"}
	}
	for range 4 {
		m.Round(context.Background())
	}
	if len(f.deleted) != 0 {
		t.Fatal("timeout quorum ignored")
	}
}

func TestIntervalJitterIsBounded(t *testing.T) {
	interval := 10 * time.Second
	for range 100 {
		got := jitter(interval)
		if got < interval || got > interval+interval/10 {
			t.Fatalf("jitter %s outside expected range", got)
		}
	}
}
