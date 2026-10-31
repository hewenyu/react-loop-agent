package client

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hewenyu/react-loop-agent/internal/agent"
	"github.com/hewenyu/react-loop-agent/internal/api"
	"github.com/hewenyu/react-loop-agent/internal/store"
)

func TestNewWithoutDaemon(t *testing.T) {
	t.Setenv(EnvAddr, "")
	t.Setenv(EnvToken, "")
	t.Setenv(store.HomeEnv, t.TempDir())

	if _, err := New(); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("err = %v, want ErrNoDaemon", err)
	}
}

func TestNewFromTheDaemonRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvAddr, "")
	t.Setenv(store.HomeEnv, home)

	if err := store.WriteDaemon(home, store.Daemon{Addr: "127.0.0.1:9", Token: "tok"}); err != nil {
		t.Fatal(err)
	}

	c, err := New()
	if err != nil || c.Addr != "127.0.0.1:9" || c.Token != "tok" {
		t.Fatalf("New = %+v, %v", c, err)
	}
}

func TestDo(t *testing.T) {
	var got http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)

		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`{"echo":` + string(b) + `}`))
		case "/raw":
			_, _ = w.Write([]byte("\x1b[1mscreen"))
		case "/fail":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"task is done"}`))
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()

	t.Setenv(agent.EnvAgentID, "a1")

	c := &Client{Addr: strings.TrimPrefix(srv.URL, "http://"), Token: "tok", HTTP: srv.Client()}

	var out struct {
		Echo map[string]string `json:"echo"`
	}

	if err := c.Do(t.Context(), "POST", "/ok", map[string]string{"k": "v"}, &out); err != nil || out.Echo["k"] != "v" {
		t.Fatalf("Do = %+v, %v", out, err)
	}

	if got.Get("Authorization") != "Bearer tok" || got.Get(api.HeaderAgent) != "a1" || got.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", got)
	}

	var raw []byte
	if err := c.Do(t.Context(), "GET", "/raw", nil, &raw); err != nil || string(raw) != "\x1b[1mscreen" {
		t.Fatalf("raw = %q, %v", raw, err)
	}

	if got.Get("Content-Type") != "" {
		t.Fatalf("Content-Type set without a body")
	}

	if err := c.Do(t.Context(), "POST", "/fail", nil, nil); err == nil || err.Error() != "task is done" {
		t.Fatalf("err = %v, want the daemon's message", err)
	}

	if err := c.Do(t.Context(), "GET", "/other", nil, nil); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want the status", err)
	}
}

func TestDoWithTheDaemonGone(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()

	c := &Client{Addr: addr, HTTP: http.DefaultClient}
	if err := c.Do(t.Context(), "GET", "/api/ping", nil, nil); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("err = %v, want ErrNoDaemon", err)
	}
}
