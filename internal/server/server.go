// Package server serves the hub's snapshot over HTTP. It ports the
// /api/snapshot route of the former TypeScript hub/server.ts (removed in P2c;
// see git history). The browser viewer and /ws are not ported.
package server

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"agent-view/internal/hub"
	"agent-view/internal/wire"
)

// Server is a running snapshot server.
type Server struct {
	// Port is the port the server is bound to; it is the chosen port when
	// Start got port 0.
	Port int
	// URL is http://127.0.0.1:<Port>.
	URL string

	addr     *net.TCPAddr
	srv      *http.Server
	done     chan struct{} // closed when Serve has returned
	stopOnce sync.Once
}

// Start binds 127.0.0.1:port (port 0 picks a free port) and serves
// GET /api/snapshot from h. Data requests need a Host and an Origin that
// name this server, which stops DNS rebinding and cross-site reads.
func Start(h *hub.Hub, port int) (*Server, error) {
	ln, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	addr := ln.Addr().(*net.TCPAddr)
	s := &Server{
		Port: addr.Port,
		URL:  fmt.Sprintf("http://127.0.0.1:%d", addr.Port),
		addr: addr,
		done: make(chan struct{}),
	}
	s.srv = &http.Server{
		Handler:           http.HandlerFunc(s.handler(h)),
		ReadHeaderTimeout: 10 * time.Second,
		// A client that stops reading loses its connection; the hub is not
		// involved, because the reply is built before any byte is written.
		WriteTimeout: 30 * time.Second,
	}
	go func() {
		defer close(s.done)
		s.srv.Serve(ln)
	}()
	return s, nil
}

// Stop closes the listener and every connection, then returns. It does not
// wait for a client to read its reply. It is safe to call more than once.
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		s.srv.Close()
		<-s.done
	})
}

func reply(w http.ResponseWriter, status int, body string, extra ...string) {
	hd := w.Header()
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
	for i := 0; i+1 < len(extra); i += 2 {
		hd.Set(extra[i], extra[i+1])
	}
	hd.Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	w.Write([]byte(body))
}

func (s *Server) handler(h *hub.Hub) http.HandlerFunc {
	hosts := []string{fmt.Sprintf("127.0.0.1:%d", s.Port), fmt.Sprintf("localhost:%d", s.Port)}
	origins := []string{fmt.Sprintf("http://127.0.0.1:%d", s.Port), fmt.Sprintf("http://localhost:%d", s.Port)}
	contains := func(list []string, v string) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// The escaped path, as TypeScript's URL pathname: a percent-encoded
		// route is a different path.
		if r.URL.EscapedPath() != "/api/snapshot" {
			reply(w, http.StatusNotFound, "not found")
			return
		}
		originVals := r.Header.Values("Origin")
		originOK := len(originVals) == 0 || (len(originVals) == 1 && contains(origins, originVals[0]))
		if !contains(hosts, r.Host) || !originOK {
			reply(w, http.StatusForbidden, "forbidden")
			return
		}
		if r.Method != http.MethodGet {
			reply(w, http.StatusMethodNotAllowed, "method not allowed", "Allow", "GET")
			return
		}
		// The body is complete before any byte is written, so no hub lock is
		// held while a slow client reads.
		body, err := snapshotBody(h)
		if err != nil {
			reply(w, http.StatusInternalServerError, "internal error")
			return
		}
		hd := w.Header()
		hd.Set("Cache-Control", "no-store")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Content-Type", "application/json")
		hd.Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
	}
}

// snapshotBody returns {"hosts":[…],"envelopes":[…]}.
func snapshotBody(h *hub.Hub) ([]byte, error) {
	hosts := h.Hosts()
	envs := h.SnapshotAll()
	if hosts == nil {
		hosts = []wire.HostStatus{}
	}
	var buf bytes.Buffer
	buf.WriteString(`{"hosts":`)
	hb, err := wire.MarshalNoEscape(hosts)
	if err != nil {
		return nil, err
	}
	buf.Write(hb)
	buf.WriteString(`,"envelopes":[`)
	for i, e := range envs {
		if i > 0 {
			buf.WriteByte(',')
		}
		eb, err := e.MarshalJSON()
		if err != nil {
			return nil, err
		}
		buf.Write(eb)
	}
	buf.WriteString(`]}`)
	return buf.Bytes(), nil
}
