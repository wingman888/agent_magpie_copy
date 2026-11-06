//go:build !darwin && !windows

package netproxy

// readSystemProxy: Linux has no system proxy setting magpie reads; the
// environment covers it.
func readSystemProxy() sysProxy { return sysProxy{} }
