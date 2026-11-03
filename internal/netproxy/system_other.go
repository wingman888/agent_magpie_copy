//go:build !darwin && !windows

package netproxy

// platformSystem: Linux has no system-wide proxy setting that applications
// read, so only the environment applies there.
func platformSystem() system { return system{} }
