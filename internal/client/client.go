// Package client talks to the running daemon.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/hewenyu/react-loop-agent/internal/store"
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
	addr, tok := os.Getenv("RLA_ADDR"), os.Getenv("RLA_TOKEN")

	if addr == "" || tok == "" {
		home, err := store.Home()
		if err != nil {
			return nil, err
		}

		d, err := store.ReadDaemon(home)
		if err != nil {
			return nil, ErrNoDaemon
		}

		addr, tok = d.Addr, d.Token
	}

	return &Client{Addr: addr, Token: tok, HTTP: &http.Client{Timeout: 30 * time.Second}}, nil
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
	req.Header.Set("Content-Type", "application/json")

	if id := os.Getenv("RLA_AGENT_ID"); id != "" {
		req.Header.Set("X-RLA-Agent", id)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoDaemon, err)
	}
	defer resp.Body.Close()

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
