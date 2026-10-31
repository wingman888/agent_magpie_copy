package netproxy

import "golang.org/x/sys/windows/registry"

func readWindowsRegistry() (server, override string, enabled bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return "", "", false
	}
	defer k.Close()
	if v, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		enabled = v != 0
	}
	server, _, _ = k.GetStringValue("ProxyServer")
	override, _, _ = k.GetStringValue("ProxyOverride")
	return
}
