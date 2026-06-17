package control

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		line string
		want Request
	}{
		{
			name: "connect with node",
			line: `{"id":7,"cmd":"connect","profile":"p1","node":"n3"}`,
			want: Request{ID: 7, Cmd: CmdConnect, Profile: "p1", Node: "n3"},
		},
		{
			name: "import_subscription",
			line: `{"id":1,"cmd":"import_subscription","url":"https://sub.example.com/x","name":"Home"}`,
			want: Request{ID: 1, Cmd: CmdImportSubscription, URL: "https://sub.example.com/x", Name: "Home"},
		},
		{
			name: "set_routing",
			line: `{"id":2,"cmd":"set_routing","mode":"global"}`,
			want: Request{ID: 2, Cmd: CmdSetRouting, Mode: "global"},
		},
		{
			name: "status no fields",
			line: `{"id":99,"cmd":"status"}`,
			want: Request{ID: 99, Cmd: CmdStatus},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decodeRequest([]byte(c.line))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got != c.want {
				t.Errorf("decode = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestDecodeRequestBadJSON(t *testing.T) {
	if _, err := decodeRequest([]byte(`{"id":1,`)); err == nil {
		t.Error("expected error on truncated json")
	}
}

func TestResponseMarshalShape(t *testing.T) {
	// A success response with data echoes id and ok and carries the data object.
	resp, err := newResult(7, State{State: StateConnecting, Node: "n3"})
	if err != nil {
		t.Fatalf("newResult: %v", err)
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	want := `{"id":7,"ok":true,"data":{"state":"connecting","node":"n3"}}`
	if got != want {
		t.Errorf("response =\n %s\nwant %s", got, want)
	}

	// An error response carries ok:false and the message, no data.
	eb, _ := json.Marshal(newError(7, "profile not found"))
	if g := string(eb); g != `{"id":7,"ok":false,"error":"profile not found"}` {
		t.Errorf("error response = %s", g)
	}

	// An empty success (remove_profile) has no data field.
	ze, _ := json.Marshal(newResult0(3))
	if g := string(ze); g != `{"id":3,"ok":true}` {
		t.Errorf("empty response = %s", g)
	}
}

func TestMarshalEventMergesFields(t *testing.T) {
	cases := []struct {
		name string
		ev   string
		body any
		want string
	}{
		{
			name: "state",
			ev:   EventState,
			body: stateEvent{State: StateConnected, Node: "n3"},
			want: `{"event":"state","state":"connected","node":"n3"}`,
		},
		{
			name: "traffic",
			ev:   EventTraffic,
			body: TrafficEvent{Up: 10240, Down: 51200, UpRate: 2048, DownRate: 8192},
			want: `{"event":"traffic","up":10240,"down":51200,"up_rate":2048,"down_rate":8192}`,
		},
		{
			name: "log",
			ev:   EventLog,
			body: LogEvent{Level: LogInfo, Msg: "started"},
			want: `{"event":"log","level":"info","msg":"started"}`,
		},
		{
			name: "empty body still names the event",
			ev:   EventState,
			body: struct{}{},
			want: `{"event":"state"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := marshalEvent(c.ev, c.body)
			if err != nil {
				t.Fatalf("marshalEvent: %v", err)
			}
			if !bytes.HasSuffix(got, []byte("\n")) {
				t.Fatalf("event line not newline-terminated: %q", got)
			}
			line := strings.TrimRight(string(got), "\n")
			if line != c.want {
				t.Errorf("event =\n %s\nwant %s", line, c.want)
			}
			// And it must be a single valid JSON object.
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Errorf("event is not valid json: %v", err)
			}
		})
	}
}

func TestMarshalLineSingleObjectPerLine(t *testing.T) {
	line, err := marshalLine(Request{ID: 1, Cmd: CmdStatus})
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(line, []byte("\n")); n != 1 {
		t.Errorf("line has %d newlines, want exactly 1", n)
	}
	if line[len(line)-1] != '\n' {
		t.Error("line must end with newline")
	}
}

func TestRequestScannerSkipsBlankLines(t *testing.T) {
	in := "\n  \n" + `{"id":1,"cmd":"status"}` + "\n\n" + `{"id":2,"cmd":"disconnect"}` + "\n"
	sc := newRequestScanner(strings.NewReader(in))
	var ids []int64
	for {
		line, ok := sc.next()
		if !ok {
			break
		}
		req, err := decodeRequest(line)
		if err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		ids = append(ids, req.ID)
	}
	if err := sc.err(); err != nil {
		t.Fatalf("scanner err: %v", err)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Errorf("ids = %v, want [1 2]", ids)
	}
}

func TestRequestScannerLongLine(t *testing.T) {
	// A link far longer than bufio's default 64 KiB token must still scan.
	long := strings.Repeat("a", 200*1024)
	in := `{"id":1,"cmd":"import_link","link":"vless://` + long + `"}` + "\n"
	sc := newRequestScanner(strings.NewReader(in))
	line, ok := sc.next()
	if !ok {
		t.Fatalf("scanner dropped long line: %v", sc.err())
	}
	if _, err := decodeRequest(line); err != nil {
		t.Fatalf("decode long line: %v", err)
	}
}
