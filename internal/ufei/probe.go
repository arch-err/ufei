package ufei

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"time"
)

type Result struct {
	Outcome  string
	Duration time.Duration
	Err      error
}

func RunProbe(parent context.Context, p Probe) Result {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, p.duration)
	defer cancel()
	var err error
	host := p.IP
	if host == "" {
		host = p.Domain
	}
	switch p.Protocol {
	case "http", "https":
		dialer := &net.Dialer{}
		tr := &http.Transport{
			// A new connection must pass through OVN's source-IP selection each time.
			DisableKeepAlives: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if p.IP != "" {
					_, port, e := net.SplitHostPort(addr)
					if e != nil {
						return nil, e
					}
					addr = net.JoinHostPort(p.IP, port)
				}
				return dialer.DialContext(ctx, network, addr)
			},
		}
		defer tr.CloseIdleConnections()
		client := http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
		if e != nil {
			err = e
			break
		}
		var resp *http.Response
		resp, err = client.Do(req)
		if err == nil {
			// Count a stalled body as a timeout, while bounding bytes from untrusted targets.
			_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if err == nil && (resp.StatusCode < 200 || resp.StatusCode >= 400) {
				err = fmt.Errorf("HTTP status %d", resp.StatusCode)
			}
		}
	case "tcp":
		var conn net.Conn
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(p.Port)))
		if err == nil {
			conn.Close()
		}
	case "icmp":
		// iputils ping uses unprivileged datagram ICMP sockets when ping_group_range permits it.
		// CommandContext enforces sub-second deadlines too, without invoking a shell.
		cmd := exec.CommandContext(ctx, "ping", "-n", "-c", "1", "--", host)
		cmd.WaitDelay = 100 * time.Millisecond
		output, e := cmd.CombinedOutput()
		err = e
		if e != nil {
			err = fmt.Errorf("ping: %w: %s", e, output)
		}
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
		var ne net.Error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			outcome = "timeout"
		}
		if parent.Err() != nil {
			outcome = "canceled"
		}
	}
	return Result{outcome, time.Since(start), err}
}
