//go:build windows

package netproxy

import "golang.org/x/sys/windows/registry"

// readSystem reads the manual proxy from "Internet Options". A failure, or a
// disabled proxy, means there is no system proxy.
func readSystem() sysProxy {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return sysProxy{}
	}
	defer k.Close()
	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled != 1 {
		return sysProxy{}
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return sysProxy{}
	}
	override, _, _ := k.GetStringValue("ProxyOverride")
	return sysProxy{URL: parseWinProxyServer(server), Except: splitList(override, ";")}
}
