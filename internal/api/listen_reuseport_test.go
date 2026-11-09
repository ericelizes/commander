package api

import (
	"net"
	"testing"
)

func TestListenSharesPortWithRestartingDaemon(t *testing.T) {
	old, err := reusePortListen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	port := old.Addr().(*net.TCPAddr).Port

	next, err := listen(port)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if got := next.Addr().(*net.TCPAddr).Port; got != port {
		t.Fatalf("new daemon drifted to port %d, want %d", got, port)
	}

	// The old daemon exiting leaves the new listener serving.
	old.Close()
	go func() {
		if c, err := next.Accept(); err == nil {
			c.Close()
		}
	}()
	c, err := net.Dial("tcp", next.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestListenMovesPastForeignListener(t *testing.T) {
	foreign, err := net.Listen("tcp", "127.0.0.1:0") // no SO_REUSEPORT
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	port := foreign.Addr().(*net.TCPAddr).Port
	ln, err := listen(port)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ln.Addr().(*net.TCPAddr).Port == port {
		t.Fatal("shared a port with a listener that did not opt in")
	}
}
