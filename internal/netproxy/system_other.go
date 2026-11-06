//go:build !darwin && !windows

package netproxy

// readOSL3x: on Linux the system has no proxy setting of its own; only the
// environment applies.
func readOSL3x() sysProxyL3x { return sysProxyL3x{} }
