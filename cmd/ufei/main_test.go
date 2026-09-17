package main

import (
	"net"
	"strings"
	"testing"
)

func TestRunReturnsListenerFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	t.Setenv("UFEI_LISTEN_ADDRESS", listener.Addr().String())
	t.Setenv("UFEI_PROBES", `[{"name":"local","protocol":"tcp","ip":"127.0.0.1","port":1,"timeout":"10ms"}]`)
	if err := run(); err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("expected listener failure, got %v", err)
	}
}
