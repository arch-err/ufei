package ufei

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHTTPDeadlineIncludesBody(t *testing.T) {
	for _, body := range []bool{false, true} {
		t.Run(strconv.FormatBool(body), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if body {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer s.Close()
			r := RunProbe(context.Background(), Probe{Protocol: "http", URL: s.URL, duration: 50 * time.Millisecond})
			if r.Outcome != "timeout" || r.Duration > 500*time.Millisecond {
				t.Fatalf("deadline not enforced: %+v", r)
			}
		})
	}
}

func TestHTTPIPOverrideAndNewConnections(t *testing.T) {
	var remotes []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Host, "egress.test:") || r.URL.RequestURI() != "/health?q=1" {
			t.Errorf("wrong request: %s %s", r.Host, r.URL)
		}
		remotes = append(remotes, r.RemoteAddr)
		w.Write([]byte("ok"))
	}))
	defer s.Close()
	_, port, _ := net.SplitHostPort(s.Listener.Addr().String())
	p := Probe{Protocol: "http", URL: "http://egress.test:" + port + "/health?q=1", IP: "127.0.0.1", duration: time.Second}
	for range 2 {
		if r := RunProbe(context.Background(), p); r.Outcome != "success" {
			t.Fatal(r.Err)
		}
	}
	if remotes[0] == remotes[1] {
		t.Fatal("connection reused")
	}
}

func TestHTTPStatusAndNoRedirect(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "fail", 500) }))
	defer s.Close()
	p := Probe{Protocol: "http", URL: s.URL, duration: time.Second}
	if r := RunProbe(context.Background(), p); r.Outcome != "error" {
		t.Fatalf("HTTP 500 must not be timeout: %+v", r)
	}
	s.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://unreachable.invalid", 302) })
	if r := RunProbe(context.Background(), p); r.Outcome != "success" {
		t.Fatalf("redirect should not be followed: %+v", r)
	}
}

func TestTLSVerification(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer s.Close()
	r := RunProbe(context.Background(), Probe{Protocol: "https", URL: s.URL, duration: 10 * time.Second})
	var unknownAuthority x509.UnknownAuthorityError
	if r.Outcome != "error" || !errors.As(r.Err, &unknownAuthority) {
		t.Fatalf("expected certificate verification failure: %+v", r)
	}
}

func TestTCPAndCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		c, e := listener.Accept()
		if e == nil {
			c.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	p := Probe{Protocol: "tcp", IP: "127.0.0.1", Port: port, duration: time.Second}
	if r := RunProbe(context.Background(), p); r.Outcome != "success" {
		t.Fatal(r.Err)
	}
	listener.Close()
	if r := RunProbe(context.Background(), p); r.Outcome != "error" {
		t.Fatalf("refusal must not be timeout: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := RunProbe(ctx, p); r.Outcome != "canceled" {
		t.Fatalf("shutdown counted as failure: %+v", r)
	}
}
