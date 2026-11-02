//go:build !darwin && !windows

package netproxy

// readSystemProxy finds nothing: elsewhere the desktop hands its proxy to
// apps as environment variables, which the environment level already reads.
func readSystemProxy() (sysProxy, error) { return sysProxy{}, nil }
