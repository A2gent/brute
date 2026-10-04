package integrationtools

import (
	"slices"
	"testing"
)

func TestBrowserChromeLaunchArgsFixHeadlessWindowSize(t *testing.T) {
	tool := &BrowserChromeTool{userDataDir: "/tmp/debug", profileDirectory: "ChromeAgent", debugPort: "9223"}
	for _, headless := range []bool{false, true} {
		args := tool.launchArgs(headless)
		if slices.Contains(args, "--headless=new") != headless || slices.Contains(args, "--window-size=1280,800") != headless {
			t.Fatalf("headless=%v: unexpected launch args %v", headless, args)
		}
		for _, arg := range []string{"--user-data-dir=/tmp/debug", "--profile-directory=ChromeAgent", "--remote-debugging-port=9223", "--remote-debugging-address=127.0.0.1"} {
			if !slices.Contains(args, arg) {
				t.Fatalf("missing %q in %v", arg, args)
			}
		}
	}
}
