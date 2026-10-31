// Package client talks to the running daemon.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"time"

	"github.com/hewenyu/react-loop-agent/internal/agent"
	"github.com/hewenyu/react-loop-agent/internal/api"
	"github.com/hewenyu/react-loop-agent/internal/store"
)

// timeout bounds one request to the daemon.
const timeout = 30 * time.Second

// How an agent reaches the daemon that started it.
const (
	EnvAddr  = "RLA_ADDR"
	EnvToken = "RLA_TOKEN"
)

// ErrNoDaemon reports that no daemon is running.
var ErrNoDaemon = errors.New("rla daemon is not running (start it with `rla daemon`, or open the app)")

// Client reaches the daemon.
type Client struct {
	Addr  string
	Token string
	HTTP  *http.Client
}

// New finds the daemon: $RLA_ADDR and $RLA_TOKEN inside an agent, else the
// record the daemon wrote.
func New() (*Client, error) {
	addr, tok := os.Getenv(EnvAddr), os.Getenv(EnvToken)

	if addr == "" || tok == "" {
		home, err := store.Home()
		if err != nil {
			return nil, err
		}

		d, err := store.ReadDaemon(home)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoDaemon
		} else if err != nil {
			return nil, err
		}

		addr, tok = d.Addr, d.Token
	}

	return &Client{Addr: addr, Token: tok, HTTP: &http.Client{Timeout: timeout}}, nil
}

// Do sends a request with a JSON body, if in is not nil, and decodes the
// JSON answer into out, if out is not nil.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader

	if raw, ok := in.([]byte); ok {
		body = bytes.NewReader(raw)
	} else if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}

		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.Addr+path, body)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.Token)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if id := os.Getenv(agent.EnvAgentID); id != "" {
		req.Header.Set(api.HeaderAgent, id)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoDaemon, err)
	}
	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}

		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}

		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}

	if out == nil {
		return nil
	}

	if raw, ok := out.(*[]byte); ok {
		*raw = b

		return nil
	}

	return json.Unmarshal(b, out)
}
