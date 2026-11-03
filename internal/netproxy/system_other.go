//go:build !darwin && !windows

package netproxy

// readSystem has nothing to read here: on Linux the environment is the only
// source of a system proxy.
func readSystem() system { return system{} }
