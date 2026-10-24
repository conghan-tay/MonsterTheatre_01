package harness

import (
	"bufio"
	"errors"
	"net"
	"testing"
	"time"
)

// echoServer answers each line with the same line.
func echoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					_, _ = c.Write([]byte(line))
				}
			}()
		}
	}()
	return l.Addr().String()
}

func roundTrip(addr string) error {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	if _, err := c.Write([]byte("ping\n")); err != nil {
		return err
	}
	_, err = bufio.NewReader(c).ReadString('\n')
	return err
}

func TestTheProxyCanBeCutAndRestored(t *testing.T) {
	p, err := startProxy(echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()

	if err := roundTrip(p.addr()); err != nil {
		t.Fatalf("through the proxy: %v", err)
	}

	// A connection open when the proxy is cut is closed, and new ones get
	// nothing through.
	open, err := net.Dial("tcp", p.addr())
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	_, _ = open.Write([]byte("ping\n"))
	if _, err := bufio.NewReader(open).ReadString('\n'); err != nil {
		t.Fatalf("before the cut: %v", err)
	}
	p.cut()
	_ = open.SetReadDeadline(time.Now().Add(time.Second))
	_, err = bufio.NewReader(open).ReadString('\n')
	var timeout net.Error
	if err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatalf("an open connection survived the cut: %v", err)
	}
	if err := roundTrip(p.addr()); err == nil {
		t.Fatal("a new connection got through while cut")
	}

	p.restore()
	if err := roundTrip(p.addr()); err != nil {
		t.Fatalf("after restoring: %v", err)
	}
}
