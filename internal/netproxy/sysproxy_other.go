//go:build !darwin && !windows

package netproxy

// readOSProxy does not read system proxy settings on this platform. Desktop
// sessions there normally pass the proxy on as environment variables.
func readOSProxy() sysProxy {
	return sysProxy{}
}
