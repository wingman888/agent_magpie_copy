//go:build !darwin && !windows

package netproxy

// platformSystemProxy reports no system proxy. On other systems the desktop
// environment passes its proxy to applications as environment variables.
func platformSystemProxy() systemConfig {
	return systemConfig{}
}
