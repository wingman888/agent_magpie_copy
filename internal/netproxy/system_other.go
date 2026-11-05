//go:build !darwin && !windows

package netproxy

// readSystemProxy does not read a system proxy on this platform. The desktop
// environment normally passes its proxy to applications as environment
// variables.
func readSystemProxy() systemConfig { return systemConfig{} }
