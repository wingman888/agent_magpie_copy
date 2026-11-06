//go:build !darwin && !windows

package netproxy

// readOS: on Linux the system has no proxy setting of its own; only the
// environment applies.
func readOS() sysProxy { return sysProxy{} }
