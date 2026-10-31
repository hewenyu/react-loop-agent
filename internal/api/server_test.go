package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hewenyu/react-loop-agent/internal/agent"
	"github.com/hewenyu/react-loop-agent/internal/sched"
)

const testToken = "secret"

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	m := agent.NewManager(agent.Config{Program: "true"}, nil)
	s := sched.New(sched.Options{Agents: m, Bot: sched.ClaudeBot{}}, sched.Saved{})
	srv := httptest.NewServer((&Server{Agents: m, Sched: s, Token: testToken, Version: "test"}).Handler())
	t.Cleanup(srv.Close)

	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path, token, body string, header map[string]string) (int, map[string]any) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	for k, v := range header {
		req.Header.Set(k, v)
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)

	return resp.StatusCode, out
}

func TestAuth(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct {
		name, path, token string
		want              int
	}{
		{"no token", "/api/ping", "", http.StatusUnauthorized},
		{"bad token", "/api/ping", "nope", http.StatusUnauthorized},
		{"header token", "/api/ping", testToken, http.StatusOK},
		{"query token", "/api/ping?token=" + testToken, "", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code, _ := do(t, srv, http.MethodGet, tt.path, tt.token, "", nil); code != tt.want {
				t.Fatalf("status = %d, want %d", code, tt.want)
			}
		})
	}
}

func TestBadRequests(t *testing.T) {
	srv := newTestServer(t)
	dir := t.TempDir()

	tests := []struct {
		name, method, path, body string
		header                   map[string]string
		want                     int
	}{
		{"task in a relative folder", "POST", "/api/tasks", `{"prompt":"p","cwd":"repo"}`, nil, http.StatusBadRequest},
		{"task in a missing folder", "POST", "/api/tasks", `{"prompt":"p","cwd":"/no/such/folder"}`, nil, http.StatusBadRequest},
		{"task without prompt", "POST", "/api/tasks", `{"prompt":" ","cwd":"` + dir + `"}`, nil, http.StatusBadRequest},
		{"task body not JSON", "POST", "/api/tasks", `{`, nil, http.StatusBadRequest},
		{"repo in a missing folder", "POST", "/api/repos", `{"path":"/no/such/folder"}`, nil, http.StatusBadRequest},
		{"unknown task action", "POST", "/api/tasks/t1/explode", "", nil, http.StatusNotFound},
		{"cancel unknown task", "POST", "/api/tasks/t1/cancel", "", nil, http.StatusNotFound},
		{"remove unknown repo", "DELETE", "/api/repos?path=/x", "", nil, http.StatusNotFound},
		{"report on unknown task", "POST", "/api/report", `{"task":"t1","status":"done"}`, nil, http.StatusNotFound},
		{"hook without agent", "POST", "/api/hook", `{"hook_event_name":"Stop"}`, nil, http.StatusBadRequest},
		{"hook for unknown agent", "POST", "/api/hook", `{"hook_event_name":"Stop"}`, map[string]string{HeaderAgent: "a1"}, http.StatusNotFound},
		{"action on unknown agent", "POST", "/api/agents/a1/interrupt", "", nil, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := do(t, srv, tt.method, tt.path, testToken, tt.body, tt.header)
			if code != tt.want {
				t.Fatalf("status = %d, want %d (%v)", code, tt.want, out)
			}

			if out["error"] == "" {
				t.Fatalf("no error message: %v", out)
			}
		})
	}
}

func TestTaskLifecycle(t *testing.T) {
	srv := newTestServer(t)
	dir := t.TempDir()

	code, task := do(t, srv, "POST", "/api/tasks", testToken, `{"prompt":"fix it","cwd":"`+dir+`"}`, nil)
	if code != http.StatusOK || task["title"] != "fix it" {
		t.Fatalf("add = %d %v", code, task)
	}

	id, _ := task["id"].(string)

	if code, out := do(t, srv, "POST", "/api/report", testToken, `{"task":"`+id+`","status":"done"}`, map[string]string{HeaderAgent: "a-other"}); code != http.StatusConflict {
		t.Fatalf("report from another agent = %d %v", code, out)
	}

	if code, out := do(t, srv, "POST", "/api/tasks/"+id+"/cancel", testToken, "", nil); code != http.StatusOK {
		t.Fatalf("cancel = %d %v", code, out)
	}

	if code, _ := do(t, srv, "POST", "/api/tasks/"+id+"/cancel", testToken, "", nil); code != http.StatusConflict {
		t.Fatalf("second cancel = %d", code)
	}

	if code, _ := do(t, srv, "DELETE", "/api/tasks/"+id, testToken, "", nil); code != http.StatusOK {
		t.Fatalf("remove = %d", code)
	}
}
