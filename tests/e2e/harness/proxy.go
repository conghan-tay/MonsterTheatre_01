package harness

import (
	"io"
	"net"
	"sync"
)

// proxy forwards TCP connections to a target, and can be cut: every open
// connection is closed and new ones are closed as soon as they arrive, as
// if the network to the target had gone. The second server reaches Redis
// through one, so a test can break that server's stream reader while the
// worker and the first server carry on.
type proxy struct {
	listener net.Listener
	target   string

	mu    sync.Mutex
	isCut bool
	conns map[net.Conn]struct{}
}

func startProxy(target string) (*proxy, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &proxy{listener: l, target: target, conns: make(map[net.Conn]struct{})}
	go p.serve()
	return p, nil
}

func (p *proxy) addr() string { return p.listener.Addr().String() }

func (p *proxy) serve() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.forward(client)
	}
}

func (p *proxy) forward(client net.Conn) {
	p.mu.Lock()
	cut := p.isCut
	p.mu.Unlock()
	if cut {
		client.Close()
		return
	}
	upstream, err := net.Dial("tcp", p.target)
	if err != nil {
		client.Close()
		return
	}
	if !p.track(client, upstream) {
		client.Close()
		upstream.Close()
		return
	}
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go pipe(upstream, client)
	go pipe(client, upstream)
	<-done
	client.Close()
	upstream.Close()
	p.untrack(client, upstream)
}

// track records a pair of connections, unless the proxy was cut meanwhile.
func (p *proxy) track(conns ...net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.isCut {
		return false
	}
	for _, c := range conns {
		p.conns[c] = struct{}{}
	}
	return true
}

func (p *proxy) untrack(conns ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range conns {
		delete(p.conns, c)
	}
}

// cut closes every connection and refuses new ones until restore.
func (p *proxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.isCut = true
	for c := range p.conns {
		c.Close()
	}
}

func (p *proxy) restore() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.isCut = false
}

func (p *proxy) close() {
	if p == nil {
		return
	}
	p.cut()
	p.listener.Close()
}
