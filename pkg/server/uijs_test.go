package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// uiJSFunc returns the source of the top-level `function <name>(` block in the
// inline script of the served index.html, for execution under node.
func uiJSFunc(t *testing.T, html, name string) string {
	t.Helper()
	start := strings.Index(html, "function "+name+"(")
	if start < 0 {
		t.Fatalf("function %s not found in UI page", name)
	}
	open := strings.Index(html[start:], "{")
	if open < 0 {
		t.Fatalf("function %s has no body", name)
	}
	depth := 0
	for i := start + open; i < len(html); i++ {
		switch html[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return html[start : i+1]
			}
		}
	}
	t.Fatalf("function %s has unbalanced braces", name)
	return ""
}

// runUIJS executes script with node and returns stdout. It skips the test when
// node is not on PATH.
func runUIJS(t *testing.T, script string) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	f := filepath.Join(t.TempDir(), "ui.js")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	return string(out)
}

func TestUIParseResponseBehaviour(t *testing.T) {
	h := newTestServer(t, "/")
	rec := doJSON(t, h, "GET", "/", "", nil)
	fn := uiJSFunc(t, rec.Body.String(), "parseResponse")

	cases := []struct {
		name   string
		status int
		body   string
		wantOK bool
		want   string
	}{
		{"html 502", 502, "<html><body>Bad Gateway</body></html>", false, "HTTP 502"},
		{"json error", 400, `{"error":"nope"}`, false, "nope"},
		{"plain text 405", 405, "405 method not allowed", false, "HTTP 405"},
		{"empty error body", 500, "", false, "HTTP 500"},
		{"json ok", 200, `{"ok":true}`, true, `{"ok":true}`},
		{"empty 2xx", 200, "", false, "HTTP 200: unexpected non-JSON response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bodyJSON, _ := json.Marshal(tc.body)
			script := fn + `
var r = {ok: ` + map[bool]string{true: "true", false: "false"}[tc.status < 400] + `, status: ` + itoa(tc.status) + `, text: function () { return Promise.resolve(` + string(bodyJSON) + `); }};
parseResponse(r).then(
  function (v) { console.log(JSON.stringify({ok: true, v: JSON.stringify(v)})); },
  function (e) { console.log(JSON.stringify({ok: false, v: e.message})); });
`
			var got struct {
				OK bool   `json:"ok"`
				V  string `json:"v"`
			}
			out := runUIJS(t, script)
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
				t.Fatalf("bad node output %q: %v", out, err)
			}
			if got.OK != tc.wantOK || got.V != tc.want {
				t.Errorf("got ok=%v %q, want ok=%v %q", got.OK, got.V, tc.wantOK, tc.want)
			}
		})
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestUIAudioFileInfoMatchesRecorderMime(t *testing.T) {
	h := newTestServer(t, "/")
	rec := doJSON(t, h, "GET", "/", "", nil)
	fn := uiJSFunc(t, rec.Body.String(), "audioFileInfo")
	script := fn + `
var cases = ['audio/webm;codecs=opus', 'audio/webm', 'audio/ogg;codecs=opus', 'audio/mp4', 'AUDIO/MP4;codecs=mp4a.40.2', 'video/mp4', ''];
console.log(JSON.stringify(cases.map(audioFileInfo)));
`
	var got []struct{ Ext, Type string }
	if err := json.Unmarshal([]byte(strings.TrimSpace(runUIJS(t, script))), &got); err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"webm", "audio/webm"}, {"webm", "audio/webm"}, {"ogg", "audio/ogg"}, {"m4a", "audio/mp4"}, {"m4a", "audio/mp4"}, {"mp4", "video/mp4"}, {"webm", "audio/webm"}}
	for i, w := range want {
		if got[i].Ext != w[0] || got[i].Type != w[1] {
			t.Errorf("case %d = %+v, want %v", i, got[i], w)
		}
	}
}
