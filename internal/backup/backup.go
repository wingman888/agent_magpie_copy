// Package backup exports magpie's configuration into one passphrase-sealed
// file and restores it on another machine.
package backup

import (
	"encoding/json"
	"errors"

	"github.com/yetone/magpie/internal/provider"
)

// Ext is the file extension of a backup file.
const Ext = ".magpie-backup"

// bundleVersion is the schema version of Bundle this build writes and reads.
const bundleVersion = 1

var (
	// ErrPassphrase means the file could not be opened with the passphrase:
	// the passphrase is wrong, or the file has been changed.
	ErrPassphrase = errors.New("备份密码错误或文件已被改动")

	// ErrNotBackup means the data is not a magpie backup at all.
	ErrNotBackup = errors.New("不是 magpie 备份文件")

	// ErrNewerVersion means the backup was made by a newer magpie.
	ErrNewerVersion = errors.New("备份由更新版本的 magpie 生成，请升级 magpie")
)

// Bundle is the plain content of a backup, before sealing.
type Bundle struct {
	Version   int                 `json:"version"`
	App       string              `json:"app,omitempty"` // the magpie build that wrote it
	Providers []provider.Provider `json:"providers,omitempty"`

	// Icons maps a picture's file name to its bytes, for providers whose
	// Icon names a custom image.
	Icons map[string][]byte `json:"icons,omitempty"`

	// Settings, Profiles and Agents are raw JSON until the parts that
	// produce and consume them are wired in. Empty means "not in the
	// backup", and Restore leaves that part alone.
	Settings json.RawMessage `json:"settings,omitempty"`
	Profiles json.RawMessage `json:"profiles,omitempty"`
	Agents   json.RawMessage `json:"agents,omitempty"`
}

// Collect gathers this machine's configuration into a Bundle. With keys
// false, no API key and no credential-like header is included. app names
// the magpie build doing the collecting.
//
// Placeholder: the full collection lands in a later step.
func Collect(keys bool, app string) (Bundle, error) {
	return Bundle{Version: bundleVersion, App: app}, nil
}
