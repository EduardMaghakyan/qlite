package sse

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
)

var jsonBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// Writer writes Server-Sent Events to an HTTP response.
type Writer interface {
	// SetHeader sets a response header. Must be called before WriteEvent.
	SetHeader(key, value string)
	// WriteEvent writes a single SSE event with the given data.
	WriteEvent(data []byte) error
	// Done writes the final [DONE] event.
	Done() error
}

type writer struct {
	w   http.ResponseWriter
	rc  *http.ResponseController
	buf []byte // reusable buffer for WriteEvent framing
}

// NewWriter creates a new SSE Writer wrapping the given ResponseWriter.
// It sets the required SSE headers.
func NewWriter(w http.ResponseWriter) Writer {
	sw := &writer{
		w:  w,
		rc: http.NewResponseController(w),
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	return sw
}

func (s *writer) SetHeader(key, value string) {
	s.w.Header().Set(key, value)
}

func (s *writer) WriteEvent(data []byte) error {
	needed := 6 + len(data) + 2 // "data: " + data + "\n\n"
	if cap(s.buf) < needed {
		s.buf = make([]byte, 0, needed)
	}
	s.buf = s.buf[:0]
	s.buf = append(s.buf, "data: "...)
	s.buf = append(s.buf, data...)
	s.buf = append(s.buf, '\n', '\n')
	if _, err := s.w.Write(s.buf); err != nil {
		return err
	}
	return s.rc.Flush()
}

func (s *writer) Done() error {
	if _, err := s.w.Write([]byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	return s.rc.Flush()
}

// WriteJSON marshals v to JSON and sends it as an SSE event.
func WriteJSON(sw Writer, v any) error {
	buf := jsonBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	if err := json.NewEncoder(buf).Encode(v); err != nil {
		jsonBufPool.Put(buf)
		return err
	}
	b := buf.Bytes()
	// json.Encoder.Encode appends a trailing newline; trim it.
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	err := sw.WriteEvent(b)
	jsonBufPool.Put(buf)
	return err
}
