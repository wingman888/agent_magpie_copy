//go:build !darwin && !windows

package netproxy

// readSystem reports no system proxy. On other systems the desktop passes its
// proxy to applications as environment variables.
func readSystem() sysConfig {
	return sysConfig{}
}
