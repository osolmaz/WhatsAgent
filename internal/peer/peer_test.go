package peer

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestConnContext checks that the other end of a Unix socket is this process, and that another
// kind of connection gives 0.
func TestConnContext(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "peer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if got := FromContext(ConnContext(context.Background(), server)); got != os.Getpid() {
		t.Errorf("peer = %d, want this process %d", got, os.Getpid())
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if got := FromContext(ConnContext(context.Background(), a)); got != 0 {
		t.Errorf("peer of a pipe = %d", got)
	}
	if FromContext(context.Background()) != 0 {
		t.Error("an empty context has a peer")
	}
}

// TestDescends checks the walk up the process tree with a real child process.
func TestDescends(t *testing.T) {
	if parent(os.Getpid()) != os.Getppid() {
		t.Fatalf("parent = %d, want %d", parent(os.Getpid()), os.Getppid())
	}
	child := exec.Command("sleep", "10")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	me, kid := os.Getpid(), child.Process.Pid
	for _, c := range []struct {
		pid, ancestor int
		want          bool
	}{
		{kid, me, true}, {kid, os.Getppid(), true}, {me, me, true},
		{me, kid, false}, {0, me, false}, {kid, 1, false}, {kid, 0, false},
	} {
		if got := Descends(c.pid, c.ancestor); got != c.want {
			t.Errorf("Descends(%d, %d) = %v", c.pid, c.ancestor, got)
		}
	}
	if parent(1<<30) != 0 {
		t.Error("a missing process has a parent")
	}
}
