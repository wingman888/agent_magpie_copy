package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// registerProtocol tells the system that magpie: links open this executable.
// It is best effort and idempotent. macOS gets the scheme from the app
// bundle's Info.plist, so there is nothing to do at run time.
func registerProtocol() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	switch runtime.GOOS {
	case "windows":
		base := `HKCU\Software\Classes\magpie`
		for _, a := range [][]string{
			{"add", base, "/ve", "/d", "URL:magpie", "/f"},
			{"add", base, "/v", "URL Protocol", "/d", "", "/f"},
			{"add", base + `\shell\open\command`, "/ve", "/d", `"` + exe + `" "%1"`, "/f"},
		} {
			if exec.Command("reg", a...).Run() != nil {
				return
			}
		}
	case "linux":
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		dir := filepath.Join(home, ".local", "share", "applications")
		if os.MkdirAll(dir, 0o755) != nil {
			return
		}
		desktop := "[Desktop Entry]\nType=Application\nName=magpie\nNoDisplay=true\n" +
			"Exec=\"" + exe + "\" %u\nMimeType=x-scheme-handler/magpie;\n"
		path := filepath.Join(dir, "magpie-url.desktop")
		if b, err := os.ReadFile(path); err == nil && string(b) == desktop {
			return
		}
		if os.WriteFile(path, []byte(desktop), 0o644) != nil {
			return
		}
		_ = exec.Command("xdg-mime", "default", "magpie-url.desktop", "x-scheme-handler/magpie").Run()
	}
}
