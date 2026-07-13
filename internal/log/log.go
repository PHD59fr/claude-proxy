package log

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type Logger struct {
	mu       sync.Mutex
	level    string
	levelNum int
	format   string
	writer   io.Writer
}

var levelMap = map[string]int{
	"debug": 0,
	"info":  1,
	"warn":  2,
	"error": 3,
}

func New(level, format string) *Logger {
	lvl := strings.ToLower(level)
	return &Logger{
		level:    lvl,
		levelNum: levelMap[lvl],
		format:   format,
		writer:   os.Stderr,
	}
}

func (l *Logger) log(level string, msg string, keysAndValues ...interface{}) {
	if !l.shouldLog(level) {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.format == "json" {
		l.logJSON(level, msg, keysAndValues...)
	} else {
		l.logText(level, msg, keysAndValues...)
	}
}

func (l *Logger) logJSON(level, msg string, keysAndValues ...interface{}) {
	entry := map[string]interface{}{
		"level": level,
		"msg":   msg,
		"time":  time.Now().UTC().Format(time.RFC3339Nano),
	}
	for i := 0; i < len(keysAndValues)-1; i += 2 {
		key := fmt.Sprintf("%v", keysAndValues[i])
		val := keysAndValues[i+1]
		if isSensitive(key) {
			val = "***"
		}
		entry[key] = val
	}
	data, err := json.Marshal(entry)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"level":"%s","msg":"%s","error":"json marshal failed"}`, level, msg))
	}
	_, _ = fmt.Fprintln(l.writer, string(data))
}

func (l *Logger) logText(level, msg string, keysAndValues ...interface{}) {
	var sb strings.Builder
	sb.WriteString(time.Now().Format("15:04:05"))
	sb.WriteString(" [")
	sb.WriteString(strings.ToUpper(level))
	sb.WriteString("] ")
	sb.WriteString(msg)
	for i := 0; i < len(keysAndValues)-1; i += 2 {
		key := fmt.Sprintf("%v", keysAndValues[i])
		val := fmt.Sprintf("%v", keysAndValues[i+1])
		if isSensitive(key) {
			val = "***"
		}
		sb.WriteString(" ")
		sb.WriteString(key)
		sb.WriteString("=")
		sb.WriteString(val)
	}
	_, _ = fmt.Fprintln(l.writer, sb.String())
}

func (l *Logger) shouldLog(level string) bool {
	lvl, ok := levelMap[level]
	if !ok {
		return false // unknown levels are never logged
	}
	return lvl >= l.levelNum
}

func isSensitive(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "key") || strings.Contains(k, "token") || strings.Contains(k, "auth") || strings.Contains(k, "secret")
}

func (l *Logger) Debug(msg string, keysAndValues ...interface{}) {
	l.log("debug", msg, keysAndValues...)
}

func (l *Logger) Info(msg string, keysAndValues ...interface{}) {
	l.log("info", msg, keysAndValues...)
}

func (l *Logger) Warn(msg string, keysAndValues ...interface{}) {
	l.log("warn", msg, keysAndValues...)
}

func (l *Logger) Error(msg string, keysAndValues ...interface{}) {
	l.log("error", msg, keysAndValues...)
}
