package launcher

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestControlAPIRejectsForeignRequestsAndGETCannotStart(t *testing.T) {
	f := newFake()
	m, _ := NewManager(f, "", "test")
	h, _ := NewAPI(m, func(string) (string, error) { return "bounded log", nil }, "C:/repo/.local/launcher.json")
	send := func(method, path, origin, host, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:4174"+path, nil)
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		r.Header.Set("X-DanmakuFlow-Token", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := send("GET", "/api/local/session", "", "127.0.0.1:4174", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var session map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &session)
	token := session["token"].(string)
	for _, c := range []struct{ method, path, origin, host, token string }{{"POST", "/api/local/start", "http://evil.test", "127.0.0.1:4174", token}, {"POST", "/api/local/start", "http://127.0.0.1:4174", "evil.test", token}, {"POST", "/api/local/start", "", "127.0.0.1:4174", token}, {"POST", "/api/local/start", "http://127.0.0.1:4174", "127.0.0.1:4174", "wrong"}, {"GET", "/api/local/start", "", "127.0.0.1:4174", token}} {
		if w := send(c.method, c.path, c.origin, c.host, c.token); w.Code < 400 {
			t.Fatalf("accepted unsafe request: %+v", c)
		}
	}
	if len(f.events) != 0 {
		t.Fatal("request caused side effect")
	}
	w = send("POST", "/api/local/start", "http://127.0.0.1:4174", "127.0.0.1:4174", token)
	if w.Code != http.StatusAccepted {
		t.Fatal(w.Body.String())
	}
	waitOperation(t, m)
	if w = send("GET", "/api/local/logs/../secret", "", "127.0.0.1:4174", token); w.Code < 400 {
		t.Fatal("arbitrary log path accepted")
	}
	if w = send("GET", "/api/local/logs/server", "", "127.0.0.1:4174", ""); w.Code != 403 {
		t.Fatal("unauthed log read")
	}
	w = send("GET", "/api/local/logs/server", "", "127.0.0.1:4174", token)
	if !strings.Contains(w.Body.String(), "bounded log") {
		t.Fatal(w.Body.String())
	}
}
