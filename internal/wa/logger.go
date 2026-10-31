package wa

import (
	"fmt"
	"log/slog"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// slogLogger implements whatsmeow's logger interface on top of slog, so WhatsAgent has one
// logging stack.
type slogLogger struct {
	log *slog.Logger
}

func newLogger(log *slog.Logger, module string) waLog.Logger {
	return slogLogger{log: log.With("module", module)}
}

func (l slogLogger) Errorf(msg string, args ...any) { l.log.Error(fmt.Sprintf(msg, args...)) }
func (l slogLogger) Warnf(msg string, args ...any)  { l.log.Warn(fmt.Sprintf(msg, args...)) }
func (l slogLogger) Infof(msg string, args ...any)  { l.log.Info(fmt.Sprintf(msg, args...)) }
func (l slogLogger) Debugf(msg string, args ...any) { l.log.Debug(fmt.Sprintf(msg, args...)) }

func (l slogLogger) Sub(module string) waLog.Logger {
	return slogLogger{log: l.log.With("sub", module)}
}
