package main

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type safeWS struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	done      chan struct{}
	closeOnce sync.Once
}

func newSafeWS(conn *websocket.Conn) *safeWS {
	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(90 * time.Second)) })
	c := &safeWS{conn: conn, done: make(chan struct{})}
	go c.heartbeat()
	return c
}
func (c *safeWS) WriteJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteJSON(v)
}
func (c *safeWS) ReadJSON(v any) error              { return c.conn.ReadJSON(v) }
func (c *safeWS) ReadMessage() (int, []byte, error) { return c.conn.ReadMessage() }
func (c *safeWS) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return c.conn.Close()
}
func (c *safeWS) heartbeat() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			if e := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); e != nil {
				_ = c.Close()
				return
			}
		}
	}
}
