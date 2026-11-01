package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFiles(t *testing.T) {
	if p, _ := File("linux", "/home/o", "/home/o/.config"); p != "/home/o/.config/systemd/user/whatsagent.service" {
		t.Errorf("linux file = %q", p)
	}
	if p, _ := File("darwin", "/Users/o", ""); p != "/Users/o/Library/LaunchAgents/"+Label+".plist" {
		t.Errorf("darwin file = %q", p)
	}
	if _, err := File("windows", "", ""); err == nil {
		t.Error("windows gave no error")
	}
}

func TestUnitAndPlist(t *testing.T) {
	spec := Spec{Bin: "/opt/my apps/whatsagent", Path: "/usr/bin:/bin", LogFile: "/tmp/a&b.log"}
	unit := Unit(spec)
	for _, want := range []string{`ExecStart="/opt/my apps/whatsagent" daemon`, "Environment=PATH=/usr/bin:/bin", "WantedBy=default.target"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit misses %q", want)
		}
	}
	if got := Unit(Spec{Bin: "/bin/w", Args: []string{"--web-host", "100.1.2.3"}}); !strings.Contains(got, "ExecStart=/bin/w daemon --web-host 100.1.2.3\n") {
		t.Errorf("unit with arguments = %s", got)
	}
	if got := Plist(Spec{Bin: "/bin/w", Args: []string{"--web-name", "a&b"}}); !strings.Contains(got,
		"<array><string>/bin/w</string><string>daemon</string><string>--web-name</string><string>a&amp;b</string></array>") {
		t.Errorf("plist with arguments = %s", got)
	}
	if !strings.Contains(Unit(Spec{Bin: "/bin/w"}), "ExecStart=/bin/w daemon") {
		t.Error("plain path was quoted")
	}
	plist := Plist(spec)
	for _, want := range []string{"<string>" + Label + "</string>", "<string>/tmp/a&amp;b.log</string>", "<key>RunAtLoad</key><true/>"} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist misses %q", want)
		}
	}
}

func TestInstallAndUninstall(t *testing.T) {
	var ran []string
	run := func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil
	}
	dir := t.TempDir()
	unit := filepath.Join(dir, "systemd", "user", "whatsagent.service")
	if err := Install("linux", unit, Spec{Bin: "/bin/w"}, 1000, run); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(unit); !strings.Contains(string(data), "ExecStart=/bin/w daemon") {
		t.Errorf("unit = %s", data)
	}
	if err := Uninstall("linux", unit, 1000, run); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Error("unit still exists")
	}
	plist := filepath.Join(dir, "LaunchAgents", Label+".plist")
	if err := Install("darwin", plist, Spec{Bin: "/bin/w"}, 501, run); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall("darwin", plist, 501, run); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now whatsagent.service",
		"systemctl --user disable --now whatsagent.service",
		"systemctl --user daemon-reload",
		"launchctl bootout gui/501/" + Label,
		"launchctl bootstrap gui/501 " + plist,
		"launchctl bootout gui/501/" + Label,
	}
	if strings.Join(ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s", strings.Join(ran, "\n"))
	}

	failing := func(string, ...string) error { return errors.New("no systemd") }
	if err := Install("linux", unit, Spec{}, 0, failing); err == nil {
		t.Error("a failing systemctl gave no error")
	}
	if err := Install("linux", "/proc/none/x.service", Spec{}, 0, run); err == nil {
		t.Error("an unwritable folder gave no error")
	}
	if err := Uninstall("linux", "/proc/none/x.service", 0, failing); err == nil {
		t.Error("a failing daemon-reload gave no error")
	}
}
