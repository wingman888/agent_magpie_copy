package netproxy

import (
	"golang.org/x/sys/windows/registry"
)

// readSystemProxy reads the manual proxy of "Internet Options" for the
// current user.
func readSystemProxy() (sysProxy, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return sysProxy{}, err
	}
	defer k.Close()
	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enable == 0 {
		return sysProxy{}, nil
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return sysProxy{}, nil
	}
	override, _, _ := k.GetStringValue("ProxyOverride")
	return parseWindows(true, server, override), nil
}
