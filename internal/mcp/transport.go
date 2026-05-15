package mcp

import (
	"bufio"
	"encoding/json"
	"io"
	"sync"
)

// Conn is a JSON-RPC 2.0 connection over an io.Reader / io.Writer pair using
// line-delimited JSON (one message per line) — the MCP stdio transport.
type Conn struct {
	r *bufio.Reader
	w io.Writer

	writeMu sync.Mutex
}

func NewConn(r io.Reader, w io.Writer) *Conn {
	return &Conn{
		r: bufio.NewReaderSize(r, 1<<20),
		w: w,
	}
}

// Read returns the next message. Returns io.EOF when the peer closes.
func (c *Conn) Read() (*Message, error) {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Write serializes m as a single line of JSON terminated by '\n'.
// Concurrent calls are safe.
func (c *Conn) Write(m *Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.w.Write(b); err != nil {
		return err
	}
	_, err = c.w.Write([]byte{'\n'})
	return err
}
