//go:build unix

package bosun

import (
	"net"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/bluebeard63/bosun/registry"
)

func TestRunShutsDownOnSignals(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			// Reserve a free port, then hand its address to Run.
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := ln.Addr().String()
			ln.Close()

			log := &probeLog{}
			app := New(WithShutdownTimeout(time.Second))
			registry.RegisterInstance[*probeLog](app.Reg, log)
			done := make(chan error, 1)
			go func() { done <- app.Run(addr) }()

			// Serving HTTP implies signal.NotifyContext is already installed.
			waitReady(t, addr)
			if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			if err := waitResult(t, done); err != nil {
				t.Fatalf("Run returned %v after %s, want nil", err, sig)
			}
			if !slices.Equal(log.snapshot(), []string{"services closed"}) {
				t.Fatalf("events = %v", log.snapshot())
			}
		})
	}
}
