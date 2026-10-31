// Package codex is the Codex adapter. It connects to the Codex app-server daemon as one more
// client, finds Codex threads, starts turns in them, relays approval requests, and runs home on
// a daemon thread.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/gorilla/websocket"
)

// Handlers receive what the daemon sends without being asked.
type Handlers struct {
	// Notify receives notifications, such as turn/completed.
	Notify func(method string, params json.RawMessage)
	// Request answers a request from the daemon, such as an approval request.
	Request func(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// Conn is one JSON-RPC connection to the app-server daemon over WebSocket.
type Conn struct {
	ws *websocket.Conn
	h  Handlers

	wmu sync.Mutex

	mu      sync.Mutex
	next    int64
	waiting map[int64]chan message
	err     error
	done    chan struct{}
}

type message struct {
	ID     *int64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Dial connects to the daemon's control socket and runs the initialize handshake.
func Dial(ctx context.Context, socket, version string, h Handlers) (*Conn, error) {
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}
	ws, resp, err := dialer.DialContext(ctx, "ws://localhost/", nil)
	if err != nil {
		return nil, fmt.Errorf("connect to the Codex app-server daemon at %s: %w", socket, err)
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	c := &Conn{ws: ws, h: h, waiting: map[int64]chan message{}, done: make(chan struct{})}
	go c.readLoop()
	params := map[string]any{"clientInfo": map[string]string{"name": "whatsagent", "version": version}}
	if err := c.Call(ctx, "initialize", params, nil); err != nil {
		c.Close()
		return nil, err
	}
	return c, c.write(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
}

// Close closes the connection.
func (c *Conn) Close() error {
	return c.ws.Close()
}

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} {
	return c.done
}

func (c *Conn) write(v any) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.WriteJSON(v)
}

// Call sends a request and decodes its result into result, which can be nil.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.next++
	id := c.next
	reply := make(chan message, 1)
	c.waiting[id] = reply
	c.mu.Unlock()
	defer c.forget(id)
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.err
	case msg := <-reply:
		return decode(method, msg, result)
	}
}

func decode(method string, msg message, result any) error {
	if msg.Error != nil {
		return fmt.Errorf("codex %s: %s", method, msg.Error.Message)
	}
	if result == nil || len(msg.Result) == 0 {
		return nil
	}
	return json.Unmarshal(msg.Result, result)
}

func (c *Conn) forget(id int64) {
	c.mu.Lock()
	delete(c.waiting, id)
	c.mu.Unlock()
}

func (c *Conn) readLoop() {
	defer close(c.done)
	for {
		var msg message
		if err := c.ws.ReadJSON(&msg); err != nil {
			c.mu.Lock()
			c.err = errors.Join(errors.New("codex daemon connection closed"), err)
			c.mu.Unlock()
			return
		}
		c.route(msg)
	}
}

func (c *Conn) route(msg message) {
	switch {
	case msg.ID != nil && msg.Method != "":
		go c.answer(msg)
	case msg.ID != nil:
		c.mu.Lock()
		reply := c.waiting[*msg.ID]
		c.mu.Unlock()
		if reply != nil {
			reply <- msg
		}
	case msg.Method != "" && c.h.Notify != nil:
		c.h.Notify(msg.Method, msg.Params)
	}
}

// answer replies to a request from the daemon.
func (c *Conn) answer(msg message) {
	var result any
	err := fmt.Errorf("WhatsAgent does not handle %s", msg.Method)
	if c.h.Request != nil {
		result, err = c.h.Request(context.Background(), msg.Method, msg.Params)
	}
	reply := map[string]any{"jsonrpc": "2.0", "id": *msg.ID}
	if err != nil {
		reply["error"] = map[string]any{"code": -32601, "message": err.Error()}
	} else {
		reply["result"] = result
	}
	_ = c.write(reply)
}
