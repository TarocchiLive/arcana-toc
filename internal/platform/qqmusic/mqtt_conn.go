package qqmusic

import (
	"github.com/gorilla/websocket"
	"io"
	"net"
	"sync"
	"time"
)

// mqttWebSocket adapts WebSocket binary messages to MQTT's byte stream.
type mqttWebSocket struct {
	socket  *websocket.Conn
	reader  io.Reader
	writeMu sync.Mutex
}

func (c *mqttWebSocket) Read(p []byte) (int, error) {
	for {
		if c.reader == nil {
			kind, reader, err := c.socket.NextReader()
			if err != nil {
				return 0, err
			}
			if kind != websocket.BinaryMessage {
				continue
			}
			c.reader = reader
		}
		n, err := c.reader.Read(p)
		if err == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}
func (c *mqttWebSocket) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.socket.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (c *mqttWebSocket) Close() error         { return c.socket.Close() }
func (c *mqttWebSocket) LocalAddr() net.Addr  { return c.socket.LocalAddr() }
func (c *mqttWebSocket) RemoteAddr() net.Addr { return c.socket.RemoteAddr() }
func (c *mqttWebSocket) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}
func (c *mqttWebSocket) SetReadDeadline(t time.Time) error { return c.socket.SetReadDeadline(t) }
func (c *mqttWebSocket) SetWriteDeadline(t time.Time) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.socket.SetWriteDeadline(t)
}
