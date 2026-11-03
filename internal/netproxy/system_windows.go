package netproxy

import (
	"golang.org/x/sys/windows/registry"
)

const windowsInternetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// readPlatformProxy reads the manual proxy from the current user's Internet
// Settings. If the proxy is not enabled, or a value is missing, there is no
// system proxy.
func readPlatformProxy() systemProxy {
	k, err := registry.OpenKey(registry.CURRENT_USER, windowsInternetSettings, registry.QUERY_VALUE)
	if err != nil {
		return systemProxy{}
	}
	defer k.Close()

	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled != 1 {
		return systemProxy{}
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return systemProxy{}
	}
	override, _, err := k.GetStringValue("ProxyOverride")
	if err != nil {
		override = ""
	}
	return parseWindowsProxy(server, override)
}
