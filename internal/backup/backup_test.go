package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

const secretKey = "sk-live-SECRET-0123456789"

func sampleBundle() Bundle {
	return Bundle{
		Version: 1,
		Providers: []provider.Provider{{
			ID:      "relay",
			Name:    "Relay Vendor",
			Key:     secretKey,
			Chat:    "https://relay.example.test/v1",
			Headers: map[string]string{"X-Api-Key": "hdr-SECRET-value"},
		}},
	}
}

func mustSeal(t *testing.T, b Bundle, pass string) []byte {
	t.Helper()
	data, err := Seal(b, pass)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	return data
}

func TestRoundTrip(t *testing.T) {
	data := mustSeal(t, sampleBundle(), "pw-1")
	got, err := Open(data, "pw-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Version != 1 || len(got.Providers) != 1 {
		t.Fatalf("bundle = %+v", got)
	}
	p := got.Providers[0]
	if p.ID != "relay" || p.Key != secretKey || p.Headers["X-Api-Key"] != "hdr-SECRET-value" {
		t.Fatalf("provider = %+v", p)
	}
}

func TestSealRejectsEmptySecret(t *testing.T) {
	if _, err := Seal(sampleBundle(), ""); err == nil {
		t.Fatal("Seal accepted an empty secret")
	}
}

func TestWrongSecret(t *testing.T) {
	data := mustSeal(t, sampleBundle(), "right")
	_, err := Open(data, "wrong")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong secret: err = %v, want ErrPassphrase", err)
	}
	_, err = Open(data, "")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("empty secret: err = %v, want ErrPassphrase", err)
	}
}

func TestLoweredIterationsRejected(t *testing.T) {
	data := mustSeal(t, sampleBundle(), "pw")
	if !bytes.Contains(data, []byte(`"iterations": 600000`)) {
		t.Fatalf("header does not carry 600000 iterations: %s", data)
	}
	tampered := bytes.Replace(data, []byte(`"iterations": 600000`), []byte(`"iterations": 100000`), 1)
	_, err := Open(tampered, "pw")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("iterations=100000: err = %v, want ErrPassphrase", err)
	}
}

func TestNotABackup(t *testing.T) {
	for _, in := range []string{"", "   \n", "hello", "{}", `{"providers":[]}`} {
		_, err := Open([]byte(in), "pw")
		if err == nil {
			t.Fatalf("Open(%q) succeeded", in)
		}
		if errors.Is(err, ErrPassphrase) || errors.Is(err, ErrNewer) {
			t.Fatalf("Open(%q) err = %v, want a not-a-backup error", in, err)
		}
		if !errors.Is(err, ErrNotBackup) {
			t.Fatalf("Open(%q) err = %v, want ErrNotBackup", in, err)
		}
		if strings.Contains(err.Error(), "passphrase") || strings.Contains(err.Error(), "口令") {
			t.Fatalf("error text leaks wording: %q", err.Error())
		}
	}
}

func TestNewerVersion(t *testing.T) {
	// kdf and the rest are deliberately absent: the version check comes first.
	in := []byte(`{"format": "magpie-backup", "version": 2}` + "\n" + "AAAA\n")
	_, err := Open(in, "pw")
	if !errors.Is(err, ErrNewer) {
		t.Fatalf("version 2: err = %v, want ErrNewer", err)
	}
	if errors.Is(err, ErrPassphrase) {
		t.Fatal("version 2 reported as a wrong secret")
	}
	if strings.Contains(err.Error(), "passphrase") || strings.Contains(err.Error(), "口令") {
		t.Fatalf("error text leaks wording: %q", err.Error())
	}
}

func TestMalformedHeaderIsPassphraseError(t *testing.T) {
	in := []byte(`{"format": "magpie-backup", "version": 1, "kdf": "pbkdf2-sha256", "iterations": "many"}` + "\nAAAA\n")
	_, err := Open(in, "pw")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("bad iterations type: err = %v, want ErrPassphrase", err)
	}
}

func TestTamperedCiphertext(t *testing.T) {
	data := mustSeal(t, sampleBundle(), "pw")
	i := bytes.IndexByte(data, '\n') + 1
	// Flip one character of the base64 body.
	if data[i] == 'A' {
		data[i] = 'B'
	} else {
		data[i] = 'A'
	}
	_, err := Open(data, "pw")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("tampered body: err = %v, want ErrPassphrase", err)
	}
}

func TestCiphertextHidesContents(t *testing.T) {
	data := mustSeal(t, sampleBundle(), "pw")
	for _, s := range []string{secretKey, "hdr-SECRET-value", "relay.example.test", "Relay Vendor", "relay"} {
		if bytes.Contains(data, []byte(s)) {
			t.Fatalf("backup file contains plaintext %q", s)
		}
	}
}

func TestSplitHeaderIsPassphraseError(t *testing.T) {
	// A header whose first line was broken across lines is a changed file,
	// not a foreign one.
	data := mustSeal(t, sampleBundle(), "pw")
	tampered := bytes.Replace(data, []byte(`"iterations": 600000,`), []byte("\n\"iterations\": 600000,"), 1)
	if bytes.Equal(tampered, data) {
		t.Fatal("test setup did not alter the header")
	}
	_, err := Open(tampered, "pw")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("split header: err = %v, want ErrPassphrase", err)
	}
	if errors.Is(err, ErrNotBackup) || errors.Is(err, ErrNewer) {
		t.Fatalf("split header reported as %v", err)
	}
}

func TestOpenReturnsHeaderVersion(t *testing.T) {
	got, err := Open(mustSeal(t, sampleBundle(), "pw"), "pw")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Version != 1 {
		t.Fatalf("Version = %d, want 1", got.Version)
	}
}

func TestNullSectionsAreAbsent(t *testing.T) {
	isolate(t)
	if err := settings.Save(settings.Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1,
		Providers: []provider.Provider{{ID: "nul", Name: "Nul", Chat: "https://nul.example.test/v1", Key: "k"}},
		Settings:  json.RawMessage("null"),
	}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Settings {
		t.Fatal("null settings reported as restored")
	}
	if s := settings.Load(); s.Theme != "dark" {
		t.Fatalf("local settings overwritten by null: %+v", s)
	}
}

func TestDuplicateIDsCountedOnce(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "dup", Name: "First", Chat: "https://first.example.test/v1", Key: "k1"},
		{ID: "dup", Name: "Second", Chat: "https://second.example.test/v1", Key: "k2"},
	}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Added != 1 || res.Replaced != 0 {
		t.Fatalf("Added=%d Replaced=%d, want 1/0", res.Added, res.Replaced)
	}
	if got := findProvider(t, "dup"); got.Name != "Second" || got.Key != "k2" {
		t.Fatalf("provider = %+v, want last entry", got)
	}
}
