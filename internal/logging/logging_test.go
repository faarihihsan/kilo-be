package logging

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"
)

func TestHandlerFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelDebug)

	ctx := WithTraceID(context.Background(), "328a93deb878e5c461191a9b28d88f13")
	logger.InfoContext(ctx, "hello", slog.String("k", "v"))

	line := strings.TrimSuffix(buf.String(), "\n")
	re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}[+-]\d{2}:\d{2} trace-id=328a93deb878e5c461191a9b28d88f13 INFO hello k=v$`)
	if !re.MatchString(line) {
		t.Errorf("line = %q", line)
	}
}

func TestHandlerWithoutTraceID(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, slog.LevelInfo).Info("startup")
	if got := buf.String(); !strings.Contains(got, "trace-id=- INFO startup") {
		t.Errorf("line = %q", got)
	}
}

func TestHandlerTraceIDFromLoggerAttr(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo).With(slog.String("request_id", "rid-9"))
	logger.Info("hello")
	line := buf.String()
	if !strings.Contains(line, "trace-id=rid-9 INFO hello") {
		t.Errorf("line = %q", line)
	}
	if strings.Contains(line, "request_id=") {
		t.Errorf("request_id must not be repeated: %q", line)
	}
}

func TestHandlerLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)
	logger.Debug("nope")
	if buf.Len() != 0 {
		t.Errorf("debug line logged at info level: %q", buf.String())
	}
	logger.Error("boom")
	if !strings.Contains(buf.String(), "ERROR boom") {
		t.Errorf("line = %q", buf.String())
	}
}
