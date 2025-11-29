/*
Copyright 2025 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ssh

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

// wsNetConn adapts a WebSocket connection to implement net.Conn.
// This allows SSH to use WebSocket as its transport layer.
type wsNetConn struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	// reader holds a partial message reader when we haven't consumed the entire message
	reader io.Reader
	mu     sync.Mutex
}

// newWSNetConn creates a net.Conn wrapper around a WebSocket connection.
// The provided context controls the lifetime of the connection.
func newWSNetConn(ctx context.Context, ws *websocket.Conn) net.Conn {
	ctx, cancel := context.WithCancel(ctx)
	return &wsNetConn{
		ws:     ws,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Read reads data from the WebSocket connection.
// WebSocket messages are read in their entirety and buffered if the caller's
// buffer is smaller than the message.
func (c *wsNetConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If we have a partial message in buffer, read from it first
	if c.reader != nil {
		n, err := c.reader.Read(b)
		if err == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			// Fall through to read next message
		} else if err != nil {
			return n, err
		} else {
			return n, nil
		}
	}

	// Read next WebSocket message
	_, reader, err := c.ws.Reader(c.ctx)
	if err != nil {
		return 0, err
	}
	c.reader = reader
	return c.reader.Read(b)
}

// Write writes data to the WebSocket connection as a binary message.
func (c *wsNetConn) Write(b []byte) (int, error) {
	err := c.ws.Write(c.ctx, websocket.MessageBinary, b)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

// Close closes the WebSocket connection with a normal closure status.
func (c *wsNetConn) Close() error {
	c.cancel()
	return c.ws.Close(websocket.StatusNormalClosure, "closing")
}

// LocalAddr returns a placeholder local address.
// WebSocket doesn't expose the underlying connection's local address.
func (c *wsNetConn) LocalAddr() net.Addr {
	return wsAddr{s: "websocket-local"}
}

// RemoteAddr returns a placeholder remote address.
// WebSocket doesn't expose the underlying connection's remote address.
func (c *wsNetConn) RemoteAddr() net.Addr {
	return wsAddr{s: "websocket-remote"}
}

// SetDeadline is a no-op. Deadlines are controlled via context.
func (c *wsNetConn) SetDeadline(t time.Time) error {
	return nil
}

// SetReadDeadline is a no-op. Read deadlines are controlled via context.
func (c *wsNetConn) SetReadDeadline(t time.Time) error {
	return nil
}

// SetWriteDeadline is a no-op. Write deadlines are controlled via context.
func (c *wsNetConn) SetWriteDeadline(t time.Time) error {
	return nil
}

// wsAddr implements net.Addr for WebSocket connections.
type wsAddr struct {
	s string
}

func (a wsAddr) Network() string { return "websocket" }
func (a wsAddr) String() string  { return a.s }
