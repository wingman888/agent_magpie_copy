package provider

// Raw access to providers.json for backup and restore. Unlike All, nothing
// here adds preset defaults or the signed-in accounts: the entries come back
// exactly as the file keeps them, so a backup restores what was on disk.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Stored returns the entries of providers.json as they are on disk. An entry
// with no base URL is the model picks of a signed-in account (see All).
func Stored() []Provider {
	return load().Providers
}

// StoredStrict is Stored, but a file that cannot be read or parsed is an
// error rather than an empty list. A missing file is an empty list.
func StoredStrict() ([]Provider, error) {
	b, err := os.ReadFile(Path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 providers.json 失败: %w", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("providers.json 无法解析，未导出或覆盖: %w", err)
	}
	return f.Providers, nil
}

// PutStored writes ps as the whole of providers.json, after the icons they
// refer to are in place. Callers merge with Stored first.
func PutStored(ps []Provider) error {
	return store(file{Providers: ps})
}
