package logger

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelError
)

type Logger struct {
	mu    sync.Mutex
	level Level
	debug *log.Logger
	info  *log.Logger
	err   *log.Logger
}

func New(level string) *Logger {
	l := &Logger{level: parseLevel(level)}
	l.debug = log.New(os.Stdout, "DEBUG ", log.LstdFlags|log.Lmsgprefix)
	l.info = log.New(os.Stdout, "INFO  ", log.LstdFlags|log.Lmsgprefix)
	l.err = log.New(os.Stderr, "ERROR ", log.LstdFlags|log.Lmsgprefix)
	return l
}

func parseLevel(s string) Level {
	switch strings.ToLower(s) {
	case "debug":
		return LevelDebug
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

func (l *Logger) Debugf(format string, args ...any) {
	l.log(LevelDebug, l.debug, format, args...)
}

func (l *Logger) Infof(format string, args ...any) {
	l.log(LevelInfo, l.info, format, args...)
}

func (l *Logger) Errorf(format string, args ...any) {
	l.log(LevelError, l.err, format, args...)
}

func (l *Logger) log(min Level, w *log.Logger, format string, args ...any) {
	l.mu.Lock()
	level := l.level
	l.mu.Unlock()

	if min < level {
		return
	}
	_ = w.Output(2, fmt.Sprintf(format, args...))
}
