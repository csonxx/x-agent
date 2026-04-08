package mcp

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type websocketClientTransport struct {
	Endpoint string
	Header   http.Header
	Dialer   *websocket.Dialer
}

func (t *websocketClientTransport) Connect(ctx context.Context) (sdkmcp.Connection, error) {
	dialer := t.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}

	conn, resp, err := dialer.DialContext(ctx, t.Endpoint, cloneHTTPHeader(t.Header))
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}

	return &websocketConn{conn: conn}, nil
}

type websocketConn struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	closeOnce sync.Once
	onClose   func()
}

func (c *websocketConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	resetDeadline := applyReadDeadline(c.conn, ctx)
	defer resetDeadline()

	for {
		msgType, data, err := c.conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}
		return jsonrpc.DecodeMessage(data)
	}
}

func (c *websocketConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	if ctx == nil {
		ctx = context.Background()
	}

	data, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return err
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if deadline, ok := ctx.Deadline(); ok {
		if err := c.conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
	} else if err := c.conn.SetWriteDeadline(time.Time{}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

func (c *websocketConn) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
		closeErr = c.conn.Close()
	})
	return closeErr
}

func (c *websocketConn) SessionID() string {
	return ""
}

func cloneHTTPHeader(header http.Header) http.Header {
	if header == nil {
		return nil
	}
	return header.Clone()
}

func applyReadDeadline(conn *websocket.Conn, ctx context.Context) func() {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
		return func() {
			_ = conn.SetReadDeadline(time.Time{})
		}
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetReadDeadline(time.Now())
		case <-done:
		}
	}()
	return func() {
		close(done)
		_ = conn.SetReadDeadline(time.Time{})
	}
}
