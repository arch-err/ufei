package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"ufei/internal/ufei"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("ufei stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := ufei.LoadConfig()
	if err != nil {
		return err
	}
	var api ufei.EgressAPI
	if len(cfg.EgressIPs) > 0 {
		api, err = ufei.NewKube(cfg)
		if err != nil {
			return err
		}
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	monitor := ufei.NewMonitor(cfg, api, ufei.NewMetrics(reg, cfg.MetricsPrefix))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	// Reachability is the monitored condition, not the health of this exporter.
	for _, path := range []string{"/healthz", "/readyz"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	}
	server := &http.Server{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	done := make(chan struct{})
	go func() { defer close(done); monitor.Run(ctx) }()
	slog.Info("ufei started", "listen", cfg.Listen, "metrics_prefix", cfg.MetricsPrefix, "probes", len(cfg.Probes), "egressips", cfg.EgressIPs, "recovery", cfg.RecoveryEnabled, "startup_grace", cfg.Cooldown)
	select {
	case <-ctx.Done():
	case err = <-serverErr:
		cancel()
	}
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	server.Shutdown(shutdown)
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
