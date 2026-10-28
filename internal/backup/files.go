package backup

import (
	"encoding/json"
	"io"
	"os"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// loadSettings reads the settings file; false when there is none.
func loadSettings() (settings.Settings, bool) {
	b, err := os.ReadFile(settings.Path())
	if err != nil {
		return settings.Settings{}, false
	}
	var s settings.Settings
	if json.Unmarshal(b, &s) != nil {
		return settings.Settings{}, false
	}
	return settings.Load(), true
}

// readIcon reads a stored picture, refusing one over the size limit.
func readIcon(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, provider.MaxIcon+1))
	if err != nil || len(b) > provider.MaxIcon {
		return nil, os.ErrInvalid
	}
	return b, nil
}
