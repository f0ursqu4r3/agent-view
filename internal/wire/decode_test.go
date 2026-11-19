package wire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeRoundTrip(t *testing.T) {
	for _, row := range shapeRows() {
		t.Run(row.name, func(t *testing.T) {
			line := EncodeLine(Envelope{V: SchemaVersion, Host: "h", Seq: 3, Ts: "T", Ev: row.ev})
			env, err := DecodeEnvelope(line)
			if err != nil {
				t.Fatalf("DecodeEnvelope(%q): %v", line, err)
			}
			if env.V != 1 || env.Host != "h" || env.Seq != 3 || env.Ts != "T" {
				t.Errorf("envelope fields = %+v", env)
			}
			if env.Ev.Kind() != row.ev.Kind() {
				t.Errorf("kind = %q, want %q", env.Ev.Kind(), row.ev.Kind())
			}
			if again := EncodeLine(env); string(again) != string(line) {
				t.Errorf("round trip\ngot  %q\nwant %q", again, line)
			}
		})
	}
}

func TestDecodeFieldPresence(t *testing.T) {
	end := func(body string) ToolEnd {
		t.Helper()
		env, err := DecodeEnvelope([]byte(`{"v":1,"host":"h","seq":1,"kind":"tool.end","agentId":"a","callId":"c",` + body + `"ts":"T"}`))
		if err != nil {
			t.Fatal(err)
		}
		return env.Ev.(ToolEnd)
	}
	if e := end(`"ok":null,`); e.Ok != nil || e.Writes != nil || e.ExitCode != nil {
		t.Errorf("null ok: %+v", e)
	}
	if e := end(`"ok":false,"exitCode":0,"writes":[],`); e.Ok == nil || *e.Ok || e.ExitCode == nil || *e.ExitCode != 0 || e.Writes == nil || len(e.Writes) != 0 {
		t.Errorf("empty writes: %+v", e)
	}
	env, err := DecodeEnvelope([]byte(`{"v":1,"host":"h","seq":1,"kind":"agent.upsert","agent":{"id":"a","provider":"codex","name":"n","role":"main","parentId":"p","link":"root","cwd":null,"model":"m","status":"idle","lastEventAt":"L"},"ts":"T"}`))
	if err != nil {
		t.Fatal(err)
	}
	a := env.Ev.(AgentUpsert).Agent
	if a.ParentID == nil || *a.ParentID != "p" || a.Cwd != nil || a.ProjectRoot != nil || a.Model != "m" {
		t.Errorf("agent = %+v", a)
	}
}

func TestDecodeDropsUnknownFields(t *testing.T) {
	env, err := DecodeEnvelope([]byte(`{"v":1,"host":"h","seq":1,"kind":"fs.change","extra":{"a":1},"path":"/p","op":"create","ts":"T"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"v":1,"host":"h","seq":1,"kind":"fs.change","path":"/p","op":"create","ts":"T"}` + "\n"
	if got := string(EncodeLine(env)); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecodeSourceEvent(t *testing.T) {
	se, err := DecodeSourceEvent([]byte(` {"kind":"tool.output","agentId":"a","callId":"c","chunk":"x","truncated":true,"ts":"T"} `))
	if err != nil {
		t.Fatal(err)
	}
	want := SourceEvent{Ts: "T", Ev: ToolOutput{AgentID: "a", CallID: "c", Chunk: "x", Truncated: true}}
	if !reflect.DeepEqual(se, want) {
		t.Errorf("got %#v, want %#v", se, want)
	}
	for _, bad := range []string{`{"kind":"fs.change"}`, `{"ts":"T"}`, `[]`, `{"kind":"fs.change","ts":"T"} x`, `{"kind":"tool.start","call":3,"ts":"T"}`,
		`{"kind":"tool.start","call":null,"ts":"T"}`, `{"kind":"agent.upsert","ts":"T"}`, `{"kind":"coverage","coverage":null,"ts":"T"}`} {
		if _, err := DecodeSourceEvent([]byte(bad)); err == nil {
			t.Errorf("DecodeSourceEvent(%q) returned no error", bad)
		}
	}
}

func TestDecodeUnknownKind(t *testing.T) {
	line := `{"v":1,"host":"h","seq":1,"kind":"future.thing","x":[1,2],"ts":"T"}`
	env, err := DecodeEnvelope([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := env.Ev.(Raw)
	if !ok {
		t.Fatalf("event is %T, want Raw", env.Ev)
	}
	if raw.Kind() != "future.thing" || raw.KindName != "future.thing" {
		t.Errorf("kind = %q", raw.Kind())
	}
	if got := string(EncodeLine(env)); got != line+"\n" {
		t.Errorf("got %q, want %q", got, line+"\n")
	}
	env.Host = "other"
	want := `{"v":1,"host":"other","seq":1,"kind":"future.thing","x":[1,2],"ts":"T"}` + "\n"
	if got := string(EncodeLine(env)); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecodeRawKeepsFieldOrder(t *testing.T) {
	env, err := DecodeEnvelope([]byte(`{"z":1, "kind":"k", "a": {"b" : [ 1 , "<&>" ]},"ts":"T","v":1,"host":"h","seq":9,"m":null}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"v":1,"host":"h","seq":9,"kind":"k","z":1,"a":{"b":[1,"<&>"]},"m":null,"ts":"T"}` + "\n"
	if got := string(EncodeLine(env)); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecodeRawEmptyFields(t *testing.T) {
	env, err := DecodeEnvelope([]byte(`{"v":1,"host":"h","seq":1,"kind":"k","ts":"T"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"v":1,"host":"h","seq":1,"kind":"k","ts":"T"}` + "\n"
	if got := string(EncodeLine(env)); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := string(EncodeLine(Envelope{V: 1, Host: "h", Seq: 1, Ts: "T", Ev: Raw{KindName: "k"}})); got != want {
		t.Errorf("zero Raw: got %q, want %q", got, want)
	}
}

func TestDecodeAcceptsIntegralNumbers(t *testing.T) {
	env, err := DecodeEnvelope([]byte(`{"v":1.0,"host":"h","seq":1e2,"kind":"k","ts":"T"}`))
	if err != nil {
		t.Fatal(err)
	}
	if env.V != 1 || env.Seq != 100 {
		t.Errorf("v = %d, seq = %d", env.V, env.Seq)
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := map[string]string{
		"v 2":           `{"v":2,"host":"h","seq":1,"kind":"hello","ts":"T"}`,
		"v string":      `{"v":"1","host":"h","seq":1,"kind":"hello","ts":"T"}`,
		"missing v":     `{"host":"h","seq":1,"kind":"hello","ts":"T"}`,
		"missing kind":  `{"v":1,"host":"h","seq":1,"ts":"T"}`,
		"missing host":  `{"v":1,"seq":1,"kind":"hello","ts":"T"}`,
		"missing seq":   `{"v":1,"host":"h","kind":"hello","ts":"T"}`,
		"missing ts":    `{"v":1,"host":"h","seq":1,"kind":"hello"}`,
		"kind null":     `{"v":1,"host":"h","seq":1,"kind":null,"ts":"T"}`,
		"host number":   `{"v":1,"host":1,"seq":1,"kind":"hello","ts":"T"}`,
		"ts number":     `{"v":1,"host":"h","seq":1,"kind":"hello","ts":5}`,
		"seq string":    `{"v":1,"host":"h","seq":"7","kind":"hello","ts":"T"}`,
		"seq fraction":  `{"v":1,"host":"h","seq":1.5,"kind":"hello","ts":"T"}`,
		"seq too large": `{"v":1,"host":"h","seq":1e300,"kind":"hello","ts":"T"}`,
		"call string":   `{"v":1,"host":"h","seq":1,"kind":"tool.start","call":"x","ts":"T"}`,
		"truncated str": `{"v":1,"host":"h","seq":1,"kind":"tool.output","agentId":"a","callId":"c","chunk":"x","truncated":"yes","ts":"T"}`,
		"no agent":      `{"v":1,"host":"h","seq":1,"kind":"agent.upsert","ts":"T"}`,
		"agent null":    `{"v":1,"host":"h","seq":1,"kind":"agent.upsert","agent":null,"ts":"T"}`,
		"no call":       `{"v":1,"host":"h","seq":1,"kind":"tool.start","ts":"T"}`,
		"call null":     `{"v":1,"host":"h","seq":1,"kind":"tool.start","call":null,"ts":"T"}`,
		"no coverage":   `{"v":1,"host":"h","seq":1,"kind":"coverage","ts":"T"}`,
		"coverage null": `{"v":1,"host":"h","seq":1,"kind":"coverage","coverage":null,"ts":"T"}`,
		"not JSON":      `not json`,
		"empty":         ``,
		"array":         `[{"v":1}]`,
		"string":        `"x"`,
		"null":          `null`,
		"trailing data": `{"v":1,"host":"h","seq":1,"kind":"k","ts":"T"} {}`,
		"truncated":     `{"v":1,"host":"h","seq":1,"kind":"k"`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			env, err := DecodeEnvelope([]byte(line))
			if err == nil {
				t.Errorf("DecodeEnvelope(%q) = %+v, want an error", line, env)
			}
		})
	}
}

// An agent.upsert, tool.start or coverage needs its object (TestDecodeRejects),
// but an empty object decodes: the object's fields are lenient.
func TestDecodeAcceptsEmptyNestedObject(t *testing.T) {
	for _, line := range []string{
		`{"v":1,"host":"h","seq":1,"kind":"agent.upsert","agent":{},"ts":"T"}`,
		`{"v":1,"host":"h","seq":1,"kind":"tool.start","call":{},"ts":"T"}`,
		`{"v":1,"host":"h","seq":1,"kind":"coverage","coverage":{},"ts":"T"}`,
	} {
		if _, err := DecodeEnvelope([]byte(line)); err != nil {
			t.Errorf("DecodeEnvelope(%q): %v", line, err)
		}
	}
}

func TestHostStatusJSON(t *testing.T) {
	b, err := json.Marshal(HostStatus{Label: "l", Kind: "remote", State: "down", Detail: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"label":"l","kind":"remote","state":"down","detail":"d"}`; string(b) != want {
		t.Errorf("got %s, want %s", b, want)
	}
	b, err = json.Marshal(HostStatus{Label: "l", Kind: "local", State: "up"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"label":"l","kind":"local","state":"up"}`; string(b) != want {
		t.Errorf("got %s, want %s", b, want)
	}
	if strings.Contains(string(b), "detail") {
		t.Error("empty detail must be omitted")
	}
}
