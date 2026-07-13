package log

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func newTestLogger(level, format string, buf *bytes.Buffer) *Logger {
	lvl := strings.ToLower(level)
	return &Logger{
		level:    lvl,
		levelNum: levelMap[lvl],
		format:   format,
		writer:   buf,
	}
}

func TestLogger_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger("info", "text", &buf)

	l.Info("server started", "addr", "127.0.0.1:3000")

	output := buf.String()
	if !strings.Contains(output, "[INFO]") {
		t.Errorf("output missing [INFO]: %q", output)
	}
	if !strings.Contains(output, "server started") {
		t.Errorf("output missing message: %q", output)
	}
	if !strings.Contains(output, "addr=127.0.0.1:3000") {
		t.Errorf("output missing key=value pair: %q", output)
	}
}

func TestLogger_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger("info", "json", &buf)

	l.Info("request processed", "status", 200)

	output := strings.TrimSpace(buf.String())

	var entry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &entry); err != nil {
		t.Fatalf("invalid JSON output: %v\noutput: %q", err, output)
	}

	if entry["level"] != "info" {
		t.Errorf("level = %v, want info", entry["level"])
	}
	if entry["msg"] != "request processed" {
		t.Errorf("msg = %v, want 'request processed'", entry["msg"])
	}
	if entry["status"] != float64(200) {
		t.Errorf("status = %v, want 200", entry["status"])
	}
	if _, ok := entry["time"]; !ok {
		t.Error("missing time field in JSON output")
	}
}

func TestLogger_SensitiveKeyMasking(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger("info", "text", &buf)

	l.Info("auth check",
		"api_key", "sk-secret-12345",
		"bearer_token", "tok-abc",
		"auth_header", "Bearer x",
		"secret_value", "hidden",
	)

	output := buf.String()
	if strings.Contains(output, "sk-secret-12345") {
		t.Error("api_key value should be masked")
	}
	if strings.Contains(output, "tok-abc") {
		t.Error("bearer_token value should be masked")
	}
	if strings.Contains(output, "Bearer x") {
		t.Error("auth_header value should be masked")
	}
	if strings.Contains(output, "hidden") {
		t.Error("secret_value value should be masked")
	}
	if !strings.Contains(output, "***") {
		t.Error("masked values should contain ***")
	}
}

func TestLogger_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger("info", "text", &buf)

	l.Debug("debug message")
	l.Info("info message")
	l.Warn("warn message")
	l.Error("error message")

	output := buf.String()

	if strings.Contains(output, "debug message") {
		t.Error("debug message should not be logged at info level")
	}
	if !strings.Contains(output, "info message") {
		t.Error("info message should be logged at info level")
	}
	if !strings.Contains(output, "warn message") {
		t.Error("warn message should be logged at info level")
	}
	if !strings.Contains(output, "error message") {
		t.Error("error message should be logged at info level")
	}
}

func TestLogger_UnknownLevel(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger("info", "text", &buf)

	// Log at a level that doesn't exist in the level map
	l.log("bogus", "should not appear")

	output := buf.String()
	if output != "" {
		t.Errorf("unknown level should produce no output, got: %q", output)
	}
}
