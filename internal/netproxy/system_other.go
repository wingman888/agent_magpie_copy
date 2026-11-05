//go:build !darwin && !windows

package netproxy

// readPlatform: on Linux only the environment counts, so there is no system proxy.
func readPlatform() system { return system{} }
