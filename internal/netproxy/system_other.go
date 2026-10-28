//go:build !darwin && !windows

package netproxy

// no system proxy here; the environment is the way
func platformRead() Proxy { return Proxy{} }
