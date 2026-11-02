package peer

import (
	"net"

	"golang.org/x/sys/unix"
)

// processOf returns the process ID of the other end of c from LOCAL_PEERPID, or 0.
func processOf(c *net.UnixConn) int {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0
	}
	pid := 0
	_ = raw.Control(func(fd uintptr) {
		if p, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); err == nil {
			pid = p
		}
	})
	return pid
}

// parent returns the parent process ID of pid from the kern.proc.pid sysctl, or 0.
func parent(pid int) int {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info.Proc.P_pid != int32(pid) {
		return 0
	}
	return int(info.Eproc.Ppid)
}
