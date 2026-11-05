//go:build !darwin && !windows

package netproxy

// readSystem has nothing to read here: on Linux only the environment counts.
func readSystem() sysProxy { return sysProxy{} }
