// Package logging provides the process logger. It renders every log line in a
// single-line text format:
//
//	2026-10-07T15:05:35.262+07:00 trace-id=<id> INFO <message>
//
// The time is local with a millisecond fraction and a numeric offset, trace-id
// is the request id (or "-" outside a request), then the level and the message.
// Attributes added with With or passed to the log call are appended as
// key=value pairs; the trace-id and its request_id alias are never repeated.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// timeFormat is the local RFC3339 form with milliseconds and a numeric offset.
const timeFormat = "2006-01-02T15:04:05.000-07:00"

type traceIDKey struct{}

// WithTraceID returns a context carrying the trace id of the request. The
// RequestID middleware sets it, so every log line written with that context
// (or with a logger derived from it) shows the same trace-id.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, id)
}

// TraceIDFrom returns the trace id in ctx, or "" when there is none.
func TraceIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(traceIDKey{}).(string)
	return id
}

// New returns a logger that writes the text format to w at level and above.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(NewHandler(w, level))
}

// Handler is the slog.Handler of the text format. Use New unless you need to
// attach the handler to something else (for example slog.NewLogLogger).
type Handler struct {
	mu     *sync.Mutex
	w      io.Writer
	level  slog.Level
	attrs  []slog.Attr
	groups []string
}

// NewHandler returns a text-format handler. level is the minimum level.
func NewHandler(w io.Writer, level slog.Level) *Handler {
	return &Handler{mu: &sync.Mutex{}, w: w, level: level}
}

var _ slog.Handler = (*Handler)(nil)

// Enabled reports whether the handler logs level.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

// Handle renders one record as a single line.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	var b strings.Builder
	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	b.WriteString(t.Format(timeFormat))
	b.WriteString(" trace-id=")
	b.WriteString(h.traceID(ctx, r))
	b.WriteByte(' ')
	b.WriteString(r.Level.String())
	b.WriteByte(' ')
	b.WriteString(r.Message)

	for _, a := range h.attrs {
		h.appendAttr(&b, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		h.appendAttr(&b, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

// WithAttrs returns a handler that appends attrs to every record.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	next := *h
	next.attrs = make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	next.attrs = append(next.attrs, h.attrs...)
	next.attrs = append(next.attrs, attrs...)
	return &next
}

// WithGroup returns a handler that prefixes attribute keys with name.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	next.groups = make([]string, len(h.groups), len(h.groups)+1)
	copy(next.groups, h.groups)
	next.groups = append(next.groups, name)
	return &next
}

// traceID resolves the trace id: context first, then the With attrs, then the
// record's own attrs. It falls back to "-" so the field is always present.
func (h *Handler) traceID(ctx context.Context, r slog.Record) string {
	if id := TraceIDFrom(ctx); id != "" {
		return id
	}
	for _, a := range h.attrs {
		if isTraceKey(a.Key) {
			if s := a.Value.String(); s != "" {
				return s
			}
		}
	}
	found := ""
	r.Attrs(func(a slog.Attr) bool {
		if isTraceKey(a.Key) {
			found = a.Value.String()
			return false
		}
		return true
	})
	if found == "" {
		return "-"
	}
	return found
}

func isTraceKey(key string) bool {
	return key == "trace_id" || key == "request_id"
}

// appendAttr writes one " key=value" pair. Group names prefix the key.
func (h *Handler) appendAttr(b *strings.Builder, a slog.Attr) {
	if isTraceKey(a.Key) {
		return // already rendered as trace-id
	}
	key := a.Key
	if len(h.groups) > 0 {
		key = strings.Join(h.groups, ".") + "." + key
	}
	b.WriteByte(' ')
	b.WriteString(key)
	b.WriteByte('=')
	b.WriteString(formatValue(a.Value))
}

func formatValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindGroup:
		attrs := v.Group()
		parts := make([]string, 0, len(attrs))
		for _, a := range attrs {
			parts = append(parts, a.Key+"="+formatValue(a.Value))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprint(v.Any())
	}
}
