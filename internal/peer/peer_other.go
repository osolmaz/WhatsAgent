//go:build !linux && !darwin

package peer

import "net"

// processOf returns 0: on this system WhatsAgent does not tell who is on the other end, so no
// session counts as a run agent.
func processOf(*net.UnixConn) int { return 0 }

func parent(int) int { return 0 }
