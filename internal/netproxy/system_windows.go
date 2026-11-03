//go:build windows

package netproxy

import "golang.org/x/sys/windows/registry"

// readSystemProxy reads the manual proxy in "Internet Options". Any failure,
// such as a missing key, means there is no system proxy.
func readSystemProxy() systemSettings {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return systemSettings{}
	}
	defer k.Close()

	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enable != 1 {
		return systemSettings{}
	}
	server, _, _ := k.GetStringValue("ProxyServer")
	override, _, _ := k.GetStringValue("ProxyOverride")
	return parseWindowsProxy(server, override)
}
