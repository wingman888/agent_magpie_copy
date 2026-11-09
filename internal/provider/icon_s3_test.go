package provider

import (
	"os"
	"strings"
	"testing"
	"time"
)

// icoS3Env points config (and so the icons dir) at a fresh temp dir.
func icoS3Env(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)
}

// icoS3PNG is a PNG-looking blob that differs by tag, so each has its own hash.
func icoS3PNG(tag string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte(tag)...)
}

// icoS3Age sets the mtime of the stored icon behind ref to two hours ago.
func icoS3Age(t *testing.T, ref string) {
	t.Helper()
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), past, past); err != nil {
		t.Fatal(err)
	}
}

// icoS3Exists reports whether the stored icon behind ref is still on disk.
func icoS3Exists(t *testing.T, ref string) bool {
	t.Helper()
	_, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:")))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestIconS3SaveAndDeletePrune(t *testing.T) {
	icoS3Env(t)
	keep, err := StoreIcon(icoS3PNG("keep"))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := StoreIcon(icoS3PNG("gone"))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := StoreIcon(icoS3PNG("fresh"))
	if err != nil {
		t.Fatal(err)
	}
	icoS3Age(t, keep)
	icoS3Age(t, gone)

	// Saving a provider that references keep prunes gone (old, unreferenced);
	// keep is referenced and fresh is recent, so both stay.
	if err := Save(Provider{ID: "icos3-a", Name: "A", Chat: "https://relay.example.com/v1", Key: "sk", Icon: keep}); err != nil {
		t.Fatal(err)
	}
	if icoS3Exists(t, gone) {
		t.Error("old unreferenced icon kept after Save")
	}
	if !icoS3Exists(t, keep) {
		t.Error("referenced icon removed by Save")
	}
	if !icoS3Exists(t, fresh) {
		t.Error("recent unreferenced icon removed by Save")
	}

	// Saving B with fresh, then ageing it, keeps it while B references it.
	if err := Save(Provider{ID: "icos3-b", Name: "B", Chat: "https://relay.example.com/v1", Key: "sk", Icon: fresh}); err != nil {
		t.Fatal(err)
	}
	icoS3Age(t, fresh)
	if err := Save(Provider{ID: "icos3-c", Name: "C", Chat: "https://relay.example.com/v1", Key: "sk"}); err != nil {
		t.Fatal(err)
	}
	if !icoS3Exists(t, fresh) {
		t.Error("icon referenced by B removed")
	}

	// Deleting A drops its reference; keep is old now and gets pruned by Delete.
	if err := Delete("icos3-a"); err != nil {
		t.Fatal(err)
	}
	if icoS3Exists(t, keep) {
		t.Error("icon no longer referenced kept after Delete")
	}
	if !icoS3Exists(t, fresh) {
		t.Error("icon referenced by B removed by Delete of A")
	}
}

func TestIconS3RestoreRefreshesModTime(t *testing.T) {
	icoS3Env(t)
	ref, err := StoreIcon(icoS3PNG("again"))
	if err != nil {
		t.Fatal(err)
	}
	icoS3Age(t, ref)
	if _, err := StoreIcon(icoS3PNG("again")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:")))
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Errorf("re-stored icon mtime not refreshed: %v", info.ModTime())
	}
}
