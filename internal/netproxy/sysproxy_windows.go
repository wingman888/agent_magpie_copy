//go:build windows

package netproxy

import "golang.org/x/sys/windows/registry"

// readOSProxy reads the manual proxy from "Internet Options". When the
// proxy is not enabled, or the registry cannot be read, there is no system proxy.
func readOSProxy() sysProxy {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return sysProxy{}
	}
	defer k.Close()

	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enable == 0 {
		return sysProxy{}
	}
	server, _, _ := k.GetStringValue("ProxyServer")
	override, _, _ := k.GetStringValue("ProxyOverride")
	return parseWindowsProxy(server, override)
}
