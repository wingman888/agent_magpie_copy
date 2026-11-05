package netproxy

import (
	"golang.org/x/sys/windows/registry"
)

// readSystemProxy reads the manual proxy from "Internet Options". Any failure,
// including a missing key, means there is no system proxy.
func readSystemProxy() systemConfig {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return systemConfig{}
	}
	defer k.Close()

	if enable, _, err := k.GetIntegerValue("ProxyEnable"); err != nil || enable != 1 {
		return systemConfig{}
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return systemConfig{}
	}
	override, _, _ := k.GetStringValue("ProxyOverride")
	return systemConfig{
		proxy:  parseWindowsProxyServer(server),
		bypass: parseBypassList(override),
	}
}
