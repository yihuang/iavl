package iavl

// Logger interface for logging
type Logger interface {
	Debug(msg string, keyvals ...interface{})
	Info(msg string, keyvals ...interface{})
	Error(msg string, keyvals ...interface{})
}

// nopLogger is a no-op logger
type nopLogger struct{}

func (l *nopLogger) Debug(msg string, keyvals ...interface{}) {}
func (l *nopLogger) Info(msg string, keyvals ...interface{})  {}
func (l *nopLogger) Error(msg string, keyvals ...interface{}) {}

// NewNopLogger creates a no-op logger
func NewNopLogger() Logger {
	return &nopLogger{}
}
