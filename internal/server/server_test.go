package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-view/internal/hub"
	"agent-view/internal/wire"
)

// fakeSource is a source the test drives by hand. Like a collector, it sends
// hello and snapshot on Subscribe.
type fakeSource struct {
	label string

	mu   sync.Mutex
	seq  int64
	subs []*func(wire.Envelope)
}

func newFake(label string) *fakeSource { return &fakeSource{label: label} }

func (f *fakeSource) Label() string { return f.label }
func (f *fakeSource) Kind() string  { return "remote" }

func (f *fakeSource) Subscribe(fn func(wire.Envelope)) func() {
	now := wire.FormatISO(wire.NowMs())
	f.mu.Lock()
	seq := f.seq
	f.mu.Unlock()
	fn(wire.Envelope{V: 1, Host: f.label, Seq: seq, Ts: now,
		Ev: wire.Hello{CollectorVersion: "0", Platform: "test", Now: now}})
	fn(wire.Envelope{V: 1, Host: f.label, Seq: seq, Ts: now, Ev: wire.Snapshot{}})
	p := &fn
	f.mu.Lock()
	f.subs = append(f.subs, p)
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.subs = slices.DeleteFunc(f.subs, func(x *func(wire.Envelope)) bool { return x == p })
	}
}

func (f *fakeSource) Status() wire.HostStatus {
	return wire.HostStatus{Label: f.label, Kind: "remote", State: "up"}
}
func (f *fakeSource) OnStatusChange(func()) func() { return func() {} }
func (f *fakeSource) Start() error                 { return nil }
func (f *fakeSource) Stop()                        {}

// emit sends an agent.upsert for the agent id, with a name of nameLen bytes.
func (f *fakeSource) emit(id string, nameLen int) {
	cwd, root := "/p", "/p"
	a := wire.Agent{ID: id, Provider: "claude", Name: id + strings.Repeat("x", nameLen), Role: "main",
		Link: "root", Cwd: &cwd, ProjectRoot: &root, Status: "working", LastEventAt: wire.FormatISO(wire.NowMs())}
	f.mu.Lock()
	f.seq++
	env := wire.Envelope{V: 1, Host: f.label, Seq: f.seq, Ts: a.LastEventAt, Ev: wire.AgentUpsert{Agent: a}}
	subs := slices.Clone(f.subs)
	f.mu.Unlock()
	for _, s := range subs {
		(*s)(env)
	}
}

// setup starts a hub with one fake source per label and a server on a free
// port. The test stops both.
func setup(t *testing.T, labels ...string) (*hub.Hub, []*fakeSource, *Server) {
	t.Helper()
	h := hub.New(hub.Options{})
	var sources []*fakeSource
	for _, l := range labels {
		s := newFake(l)
		sources = append(sources, s)
		if err := h.AddSource(s); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := Start(h, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Stop(); h.Stop() })
	return h, sources, srv
}

// do sends one request. host and origin override the headers when non-empty.
func do(t *testing.T, method, url, host, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// /api/snapshot returns hosts and envelopes with no-store and nosniff
func TestSnapshotReturnsHostsAndEnvelopesWithNoStoreAndNosniff(t *testing.T) {
	_, sources, srv := setup(t, "alpha", "beta")
	sources[1].emit("b1", 0)
	res := do(t, "GET", srv.URL+"/api/snapshot", "", "")
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := res.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Errorf("Content-Type = %q", got)
	}
	var body struct {
		Hosts     []wire.HostStatus `json:"hosts"`
		Envelopes []struct {
			Host string `json:"host"`
			Kind string `json:"kind"`
		} `json:"envelopes"`
	}
	if err := json.Unmarshal([]byte(readBody(t, res)), &body); err != nil {
		t.Fatal(err)
	}
	var labels, envs []string
	for _, h := range body.Hosts {
		labels = append(labels, h.Label)
	}
	for _, e := range body.Envelopes {
		envs = append(envs, e.Host+":"+e.Kind)
	}
	if want := []string{"alpha", "beta"}; !slices.Equal(labels, want) {
		t.Errorf("hosts = %v, want %v", labels, want)
	}
	want := []string{"alpha:hello", "alpha:snapshot", "beta:hello", "beta:snapshot"}
	if !slices.Equal(envs, want) {
		t.Errorf("envelopes = %v, want %v", envs, want)
	}
}

// An empty hub encodes both lists as [], and the encoder never escapes <, > or &.
func TestSnapshotEmptyListsAndNoHTMLEscape(t *testing.T) {
	h := hub.New(hub.Options{})
	srv, err := Start(h, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Stop(); h.Stop() })
	if got := readBody(t, do(t, "GET", srv.URL+"/api/snapshot", "", "")); got != `{"hosts":[],"envelopes":[]}` {
		t.Errorf("empty body = %s", got)
	}

	_, sources, srv2 := setup(t, "a<b>&c")
	sources[0].emit("a<1>&", 0)
	got := readBody(t, do(t, "GET", srv2.URL+"/api/snapshot", "", ""))
	if strings.Contains(got, "\\u003c") || strings.Contains(got, "\\u0026") || !strings.Contains(got, `"a<b>&c"`) {
		t.Errorf("body escapes HTML: %s", got)
	}
}

// server rejects /ws and /api/snapshot with a foreign Host or Origin (403)
// Only /api/snapshot exists now.
func TestForeignHostOrOriginGets403(t *testing.T) {
	_, _, srv := setup(t, "alpha")
	url := srv.URL + "/api/snapshot"
	forbidden := func(name string, res *http.Response) {
		t.Helper()
		if res.StatusCode != 403 {
			t.Errorf("%s: status = %d, want 403", name, res.StatusCode)
		}
		if got := readBody(t, res); got != "forbidden" {
			t.Errorf("%s: body = %q", name, got)
		}
		if got := res.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q", name, got)
		}
		if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q", name, got)
		}
	}
	forbidden("foreign host", do(t, "GET", url, fmt.Sprintf("evil.test:%d", srv.Port), ""))
	forbidden("wrong port", do(t, "GET", url, "localhost:1", ""))
	forbidden("bare host", do(t, "GET", url, "127.0.0.1", ""))
	forbidden("foreign origin", do(t, "GET", url, "", "http://evil.test"))
	forbidden("origin wrong port", do(t, "GET", url, "", "http://127.0.0.1:1"))
	// 403 comes before 405.
	forbidden("foreign host POST", do(t, "POST", url, "evil.test", ""))
	// An Origin header that is present and empty is not absent.
	req, _ := http.NewRequest("GET", url, nil)
	req.Header["Origin"] = []string{""}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	forbidden("empty origin", res)

	// Allowed Host and Origin pass.
	for _, host := range []string{"127.0.0.1", "localhost"} {
		hp := fmt.Sprintf("%s:%d", host, srv.Port)
		if res := do(t, "GET", url, hp, "http://"+hp); res.StatusCode != 200 {
			t.Errorf("%s: status = %d, want 200", hp, res.StatusCode)
		}
	}
}

// non-GET requests to data routes get 405
func TestNonGETGets405(t *testing.T) {
	_, _, srv := setup(t, "alpha")
	for _, method := range []string{"POST", "PUT", "DELETE", "HEAD"} {
		res := do(t, method, srv.URL+"/api/snapshot", "", "")
		if res.StatusCode != 405 {
			t.Errorf("%s: status = %d, want 405", method, res.StatusCode)
		}
		if got := res.Header.Get("Allow"); got != "GET" {
			t.Errorf("%s: Allow = %q", method, got)
		}
		if method != "HEAD" {
			if got := readBody(t, res); got != "method not allowed" {
				t.Errorf("%s: body = %q", method, got)
			}
		}
		if got := res.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q", method, got)
		}
	}
}

// unknown paths get 404 and the server binds 127.0.0.1 only
func TestUnknownPathsGet404AndBindLoopbackOnly(t *testing.T) {
	_, _, srv := setup(t, "alpha")
	for _, p := range []string{"/nope", "/api/other"} {
		res := do(t, "GET", srv.URL+p, "", "")
		if res.StatusCode != 404 {
			t.Errorf("%s: status = %d, want 404", p, res.StatusCode)
		}
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d", srv.Port); srv.URL != want {
		t.Errorf("URL = %q, want %q", srv.URL, want)
	}
	if srv.Port <= 0 {
		t.Errorf("Port = %d", srv.Port)
	}
	if srv.addr.IP.String() != "127.0.0.1" {
		t.Errorf("bound to %v", srv.addr)
	}
}

// GET / and GET /ws with a good Host get 404 not found.
func TestRootAndWSAre404(t *testing.T) {
	_, _, srv := setup(t, "alpha")
	for _, p := range []string{"/", "/ws"} {
		res := do(t, "GET", srv.URL+p, fmt.Sprintf("127.0.0.1:%d", srv.Port), "")
		if res.StatusCode != 404 {
			t.Errorf("%s: status = %d, want 404", p, res.StatusCode)
		}
		if got := readBody(t, res); got != "not found" {
			t.Errorf("%s: body = %q", p, got)
		}
		if got := res.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q", p, got)
		}
	}
}

// A client that sends a valid request and never reads must not block the hub
// or other requests (Review Focus 1), and Stop must not wait for it.
func TestStuckClientDoesNotBlockHub(t *testing.T) {
	h := hub.New(hub.Options{})
	src := newFake("alpha")
	if err := h.AddSource(src); err != nil {
		t.Fatal(err)
	}
	// 2,000 agents with long names: the body is far larger than the socket
	// buffers, so the stuck client's reply cannot be written in one go.
	for i := 0; i < 2000; i++ {
		src.emit(fmt.Sprintf("a%d", i), 4000)
	}
	srv, err := Start(h, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Stop(); h.Stop() })

	size := len(readBody(t, do(t, "GET", srv.URL+"/api/snapshot", "", "")))
	if size < 4<<20 {
		t.Fatalf("body is %d bytes; too small to fill the socket buffers", size)
	}

	stuck, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", srv.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer stuck.Close()
	fmt.Fprintf(stuck, "GET /api/snapshot HTTP/1.1\r\nHost: 127.0.0.1:%d\r\n\r\n", srv.Port)
	time.Sleep(100 * time.Millisecond) // let the handler start to write

	start := time.Now()
	for i := 0; i < 200; i++ {
		src.emit(fmt.Sprintf("late%d", i), 0)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("ingesting 200 envelopes took %v with a stuck client", d)
	}

	done := make(chan int, 1)
	go func() {
		res, err := http.Get(srv.URL + "/api/snapshot")
		if err != nil {
			done <- -1
			return
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body)
		done <- res.StatusCode
	}()
	select {
	case code := <-done:
		if code != 200 {
			t.Errorf("second client status = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("second client was not served within 1 s")
	}

	stopped := make(chan struct{})
	go func() { srv.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return with a stuck client")
	}
	if _, err := http.Get(srv.URL + "/api/snapshot"); err == nil {
		t.Error("server still answers after Stop")
	}
}
