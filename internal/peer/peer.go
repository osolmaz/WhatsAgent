// Package peer tells which process is on the other end of a Unix socket connection, and whether
// that process descends from another one. The daemon uses it to recognize the MCP server of an
// agent that it started itself: that server is a child of the agent's process.
package peer

import (
	"context"
	"net"
)

type pidKey struct{}

// ConnContext adds the process ID of the other end of c to ctx, for http.Server.ConnContext. It
// adds 0 when c is not a Unix socket connection or the system does not tell.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	pid := 0
	if unix, ok := c.(*net.UnixConn); ok {
		pid = processOf(unix)
	}
	return context.WithValue(ctx, pidKey{}, pid)
}

// FromContext returns the process ID that ConnContext added to ctx, or 0.
func FromContext(ctx context.Context) int {
	pid, _ := ctx.Value(pidKey{}).(int)
	return pid
}

// Descends reports whether process pid is process ancestor or one of its descendants. A
// process that ended, and process 1 or 0, never count.
func Descends(pid, ancestor int) bool {
	if ancestor <= 1 {
		return false
	}
	for depth := 0; pid > 1 && depth < 64; depth++ {
		if pid == ancestor {
			return true
		}
		pid = parent(pid)
	}
	return false
}
