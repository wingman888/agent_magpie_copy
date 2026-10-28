package netproxy

import "golang.org/x/sys/windows/registry"

func platformRead() Proxy {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return Proxy{}
	}
	defer k.Close()
	en, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil {
		return Proxy{}
	}
	server, _, _ := k.GetStringValue("ProxyServer")
	override, _, _ := k.GetStringValue("ProxyOverride")
	return parseWindows(server, override, en != 0)
}
