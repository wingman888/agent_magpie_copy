package netproxy

import (
	"golang.org/x/sys/windows/registry"
)

// readSystem reads the manual proxy from the current user's Internet Settings.
// When it is off, or the keys cannot be read, there is no system proxy.
func readSystem() sysConf {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return sysConf{}
	}
	defer k.Close()
	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enable != 1 {
		return sysConf{}
	}
	server, _, _ := k.GetStringValue("ProxyServer")
	override, _, _ := k.GetStringValue("ProxyOverride")
	return winSystem(server, override)
}
