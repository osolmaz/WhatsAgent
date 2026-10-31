package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
)

// Client talks to the daemon over its Unix socket.
type Client struct {
	http *http.Client
}

// NewClient returns a client for the daemon socket at path.
func NewClient(path string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}
	return &Client{http: &http.Client{Transport: transport}}
}

const base = "http://whatsagent"

func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil && (errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)) {
		return nil, ErrDaemon
	}
	return resp, err
}

// Tool calls a tool and decodes its result into out. An empty token calls it as the owner.
func (c *Client) Tool(ctx context.Context, token, name string, args, out any) error {
	raw, err := c.ToolRaw(ctx, token, name, args)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// ToolRaw calls a tool and returns its result as JSON.
func (c *Client) ToolRaw(ctx context.Context, token, name string, args any) (json.RawMessage, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	path := "/v1/owner"
	if token != "" {
		path = "/v1/tool"
	}
	body, err := json.Marshal(ToolRequest{Token: token, Tool: name, Args: data})
	if err != nil {
		return nil, err
	}
	var resp ToolResponse
	if err := c.post(ctx, path, body, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	return resp.Result, nil
}

func (c *Client) post(ctx context.Context, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// Permission relays a permission prompt to the owner.
func (c *Client) Permission(ctx context.Context, req PermissionRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	var resp ToolResponse
	if err := c.post(ctx, "/v1/permission", body, &resp); err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	return nil
}

// Session opens a session stream with the query fields of the check-in, such as name, harness,
// folder and push, and passes each frame to handle until the stream ends.
func (c *Client) Session(ctx context.Context, query url.Values, handle func(Frame) error) error {
	return c.stream(ctx, "/v1/session", query, handle)
}

// Watch opens a watch stream for a session name and extra chats. A since of 0 or more starts the
// watch after that event; -1 continues from the session's cursor.
func (c *Client) Watch(ctx context.Context, name string, chats []string, since int64, handle func(Frame) error) error {
	query := url.Values{"name": {name}, "chat": chats}
	if since >= 0 {
		query.Set("since", strconv.FormatInt(since, 10))
	}
	return c.stream(ctx, "/v1/watch", query, handle)
}

func (c *Client) stream(ctx context.Context, path string, query url.Values, handle func(Frame) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return readFrames(resp, handle)
}

func readFrames(resp *http.Response, handle func(Frame) error) error {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		var f Frame
		if err := json.Unmarshal(scanner.Bytes(), &f); err != nil {
			return fmt.Errorf("bad frame: %w", err)
		}
		if f.Type == "error" {
			return errors.New(f.Name)
		}
		if err := handle(f); err != nil {
			return err
		}
	}
	return scanner.Err()
}
