package provider

import (
	"os"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// Stored lists the providers the user configured with a key and URL, as
// kept in providers.json: signed-in agent accounts (subscriptions) are not
// among them.
func Stored() []Provider {
	var out []Provider
	for _, p := range All() {
		if p.Account == nil {
			out = append(out, p)
		}
	}
	return out
}

// ValidID reports whether id is one Save would accept for a provider.
func ValidID(id string) bool {
	return id != "" && id == Slug(id) && id != "magpie"
}

// IconFileName is the stored picture's file name an Icon value points at,
// or "" when the icon is a built-in one.
func IconFileName(icon string) string {
	name, ok := strings.CutPrefix(icon, iconPrefix)
	if !ok || iconName.FindString(name) == "" {
		return ""
	}
	return name
}

// RestoreIcon writes a picture under its own name unless a file of that
// name is already there or the picture is empty or over MaxIcon. It reports
// whether it wrote one.
func RestoreIcon(name string, data []byte) bool {
	path := IconFile(name)
	if path == "" || len(data) == 0 || len(data) > MaxIcon {
		return false
	}
	if _, err := os.Stat(path); err == nil {
		return false
	}
	return edit.WriteAtomic(path, data) == nil
}

// RestoreAll writes providers from a backup, matching them by id: one the
// machine has is replaced, the others added. A provider with no key at all
// (a backup made without keys) keeps the keys the machine has for it. Ids
// that are invalid, reserved, or those of a signed-in account are ignored.
// It returns the ids written, split into added and replaced, and the names
// of those left with no key.
func RestoreAll(ps []Provider) (added, replaced int, needKey []string, err error) {
	f := load()
	accounts := Accounts()
	for _, p := range ps {
		p = normalize(p)
		p.IconURL = ""
		p.Account = nil
		if !ValidID(p.ID) {
			continue
		}
		if _, ok := find(accounts, p.ID); ok {
			continue
		}
		if p.Name == "" {
			p.Name = p.ID
		}
		idx := -1
		for i := range f.Providers {
			if f.Providers[i].ID == p.ID {
				idx = i
				break
			}
		}
		if idx >= 0 && p.Key == "" && len(p.Keys) == 0 {
			old := f.Providers[idx]
			p.Key, p.KeyName, p.KeyProtocol, p.Keys = old.Key, old.KeyName, old.KeyProtocol, old.Keys
		}
		if p.Key == "" && len(p.Keys) == 0 {
			needKey = append(needKey, p.Name)
		}
		if idx >= 0 {
			f.Providers[idx] = p
			replaced++
		} else {
			f.Providers = append(f.Providers, p)
			added++
		}
	}
	if added+replaced == 0 {
		return 0, 0, nil, nil
	}
	if err := store(f); err != nil {
		return 0, 0, nil, err
	}
	return added, replaced, needKey, nil
}
