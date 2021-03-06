package log

import (
	p "github.com/jackc/pgx"
	"github.com/sirupsen/logrus"
	"strings"
	"testing"
)

type wr string

func (w *wr) Write(p []byte) (n int, err error) {
	*w = wr(p)
	return len(p), nil
}

func (w *wr) String() string {
	return string(*w)
}

func TestDBLogger(t *testing.T) {
	lr := logrus.New()
	l := NewPgxLogger(lr)

	if l.l != lr {
		t.Errorf("Invalid logrus instance %v, expected %v", l.l, lr)
	}
}

func TestDbLogger_Log(t *testing.T) {
	w := new(wr)
	lr := logrus.New()
	lr.SetLevel(logrus.TraceLevel)
	lr.SetFormatter(&logrus.TextFormatter{DisableColors: true, DisableTimestamp: true})
	lr.SetOutput(w)
	l := NewPgxLogger(lr)

	if l.l != lr {
		t.Errorf("Invalid logrus instance %v, expected %v", l.l, lr)
	}
	{
		testMsg := "test - TRACE"
		l.Log(p.LogLevelTrace, testMsg, nil)
		if !strings.Contains(w.String(), "trace") {
			t.Errorf("Could not find the log level in the log message, searching for 'trace' in %s", w.String())
		}
		if !strings.Contains(w.String(), testMsg) {
			t.Errorf("Could not find the message in the log message, searching for %s in %s", testMsg, w.String())
		}
	}
	{
		testMsg := "test - DEBUG"
		l.Log(p.LogLevelDebug, testMsg, nil)
		if !strings.Contains(w.String(), "debug") {
			t.Errorf("Could not find the log level in the log message, searching for 'debug' in %s", w.String())
		}
		if !strings.Contains(w.String(), testMsg) {
			t.Errorf("Could not find the message in the log message, searching for %s in %s", testMsg, w.String())
		}
	}
	{
		testMsg := "test - INFO"
		l.Log(p.LogLevelInfo, testMsg, nil)
		if !strings.Contains(w.String(), "info") {
			t.Errorf("Could not find the log level in the log message, searching for 'info' in %s", w.String())
		}
		if !strings.Contains(w.String(), testMsg) {
			t.Errorf("Could not find the message in the log message, searching for %s in %s", testMsg, w.String())
		}
	}
	{
		testMsg := "test - WARN"
		l.Log(p.LogLevelWarn, testMsg, nil)
		if !strings.Contains(w.String(), "warning") {
			t.Errorf("Could not find the log level in the log message, searching for 'warning' in %s", w.String())
		}
		if !strings.Contains(w.String(), testMsg) {
			t.Errorf("Could not find the message in the log message, searching for %s in %s", testMsg, w.String())
		}
	}
	{
		testMsg := "test - ERROR"
		l.Log(p.LogLevelError, testMsg, nil)
		if !strings.Contains(w.String(), "error") {
			t.Errorf("Could not find the log level in the log message, searching for 'error' in %s", w.String())
		}
		if !strings.Contains(w.String(), testMsg) {
			t.Errorf("Could not find the message in the log message, searching for %s in %s", testMsg, w.String())
		}
	}
}
