//go:build !windows

package netproxy

func readWindowsRegistry() (server, override string, enabled bool) { return "", "", false }
