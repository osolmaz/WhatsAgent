package peer

import (
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// processOf returns the process ID of the other end of c from SO_PEERCRED, or 0.
func processOf(c *net.UnixConn) int {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0
	}
	pid := 0
	_ = raw.Control(func(fd uintptr) {
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
			pid = int(cred.Pid)
		}
	})
	return pid
}

// parent returns the parent process ID of pid from /proc, or 0. The process name in the stat
// line can hold spaces and parentheses, so the fields count from after its closing parenthesis.
func parent(pid int) int {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	stat := string(data)
	fields := strings.Fields(stat[strings.LastIndexByte(stat, ')')+1:])
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}
