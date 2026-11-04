package backup

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const testPass = "correct horse battery"

// secretBundle holds strings that must never appear in a sealed file.
func secretBundle() Bundle {
	return Bundle{
		Version: bundleVersion,
		App:     "test-build",
		Providers: []provider.Provider{{
			ID:      "acme",
			Name:    "PROVNAME-3306",
			Key:     "sk-LEAKKEY-7731",
			KeyName: "work",
			Keys: []provider.KeyAccount{
				{Name: "spare", Key: "sk-MULTIKEY-9902"},
			},
			Chat:    "https://leak-host-4418.example.test/v1",
			Headers: map[string]string{"X-Api-Key": "HDRVAL-5520", "X-Team": "team-blue"},
		}},
		Icons:    map[string][]byte{"acme.png": []byte("PNGDATA-1234")},
		Settings: json.RawMessage(`{"theme":"dark"}`),
		Profiles: json.RawMessage(`[{"name":"PROFILE-LEAK-8812"}]`),
		Agents:   json.RawMessage(`{"claude":{"model":"acme/model-x"}}`),
	}
}

var (
	fixtureOnce sync.Once
	fixtureData []byte
	fixtureErr  error
)

// fixture seals secretBundle once, with the default iteration count, and
// hands out copies so tests do not share mutable state.
func fixture(t *testing.T) []byte {
	t.Helper()
	fixtureOnce.Do(func() {
		fixtureData, fixtureErr = Seal(secretBundle(), testPass)
	})
	if fixtureErr != nil {
		t.Fatalf("Seal: %v", fixtureErr)
	}
	return append([]byte(nil), fixtureData...)
}

// remap decodes a sealed file's header, applies fn, and encodes it again.
func remap(t *testing.T, sealed []byte, fn func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(sealed, &m); err != nil {
		t.Fatalf("header is not JSON: %v", err)
	}
	fn(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	return out
}

// requireErr fails unless err is "wrong passphrase or changed file".
func requireErrPassphrase(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("%s: want ErrPassphrase, got %v", what, err)
	}
}

// requireNoLeakWords checks that an error text does not mention the
// passphrase, in English or Chinese.
func requireNoLeakWords(t *testing.T, err error) {
	t.Helper()
	msg := err.Error()
	if strings.Contains(msg, "passphrase") || strings.Contains(msg, "口令") {
		t.Fatalf("error text must not mention the passphrase: %q", msg)
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	sealed := fixture(t)
	got, err := Open(sealed, testPass)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := secretBundle()
	if got.Version != want.Version || got.App != want.App {
		t.Fatalf("version/app: got %d/%q", got.Version, got.App)
	}
	if len(got.Providers) != 1 {
		t.Fatalf("providers: got %d", len(got.Providers))
	}
	p := got.Providers[0]
	if p.ID != "acme" || p.Name != "PROVNAME-3306" || p.Key != "sk-LEAKKEY-7731" {
		t.Fatalf("provider fields: %+v", p)
	}
	if p.KeyName != "work" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-MULTIKEY-9902" {
		t.Fatalf("multi-key accounts: %+v", p.Keys)
	}
	if p.Headers["X-Api-Key"] != "HDRVAL-5520" || p.Headers["X-Team"] != "team-blue" {
		t.Fatalf("headers: %v", p.Headers)
	}
	if string(got.Icons["acme.png"]) != "PNGDATA-1234" {
		t.Fatalf("icons: %v", got.Icons)
	}
	if string(got.Settings) != `{"theme":"dark"}` {
		t.Fatalf("settings: %s", got.Settings)
	}
}

func TestSealRejectsEmptyPassphrase(t *testing.T) {
	if _, err := Seal(secretBundle(), ""); err == nil {
		t.Fatal("Seal with empty passphrase: want error")
	}
}

func TestOpenEmptyPassphraseIsPassphraseError(t *testing.T) {
	_, err := Open(fixture(t), "")
	requireErrPassphrase(t, err, "empty passphrase")
}

func TestOpenWrongPassphrase(t *testing.T) {
	_, err := Open(fixture(t), "wrong passphrase")
	requireErrPassphrase(t, err, "wrong passphrase")
}

func TestLoweredIterationsRejected(t *testing.T) {
	sealed := fixture(t)
	old := []byte(`"iterations": 600000`)
	if !bytes.Contains(sealed, old) {
		t.Fatalf("sealed header lacks %s; got:\n%s", old, sealed[:min(len(sealed), 300)])
	}
	lowered := bytes.Replace(sealed, old, []byte(`"iterations": 100000`), 1)
	_, err := Open(lowered, testPass)
	requireErrPassphrase(t, err, "iterations lowered to 100000")
}

func TestTamperedHeaderFieldsRejected(t *testing.T) {
	sealed := fixture(t)
	cases := map[string]func(m map[string]any){
		"iterations raised": func(m map[string]any) { m["iterations"] = 700000 },
		"salt changed": func(m map[string]any) {
			salt, _ := base64.StdEncoding.DecodeString(m["salt"].(string))
			salt[0] ^= 0xff
			m["salt"] = base64.StdEncoding.EncodeToString(salt)
		},
		"nonce changed": func(m map[string]any) {
			nonce, _ := base64.StdEncoding.DecodeString(m["nonce"].(string))
			nonce[0] ^= 0xff
			m["nonce"] = base64.StdEncoding.EncodeToString(nonce)
		},
		"data changed": func(m map[string]any) {
			ct, _ := base64.StdEncoding.DecodeString(m["data"].(string))
			ct[len(ct)-1] ^= 0x01
			m["data"] = base64.StdEncoding.EncodeToString(ct)
		},
		"extra field": func(m map[string]any) { m["note"] = "hello" },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Open(remap(t, sealed, fn), testPass)
			requireErrPassphrase(t, err, name)
		})
	}
}

func TestDuplicateAndCaseVariantKeysRejected(t *testing.T) {
	sealed := fixture(t)
	kdf := []byte(`"kdf": "pbkdf2-sha256"`)
	if !bytes.Contains(sealed, kdf) {
		t.Fatalf("sealed header lacks kdf field")
	}
	dup := bytes.Replace(sealed, kdf, append(append([]byte{}, kdf...), []byte(`, "kdf": "pbkdf2-sha256"`)...), 1)
	_, err := Open(dup, testPass)
	requireErrPassphrase(t, err, "duplicate kdf key")

	upper := bytes.Replace(sealed, []byte(`"kdf"`), []byte(`"KDF"`), 1)
	_, err = Open(upper, testPass)
	requireErrPassphrase(t, err, "case-variant KDF key")
}

func TestNotBackupFormats(t *testing.T) {
	inputs := map[string][]byte{
		"empty":   {},
		"hello":   []byte("hello"),
		"object":  []byte("{}"),
		"array":   []byte("[1,2]"),
		"garbage": []byte{0xff, 0x00, 0x7b},
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			_, err := Open(in, testPass)
			if err == nil {
				t.Fatal("want error")
			}
			if errors.Is(err, ErrPassphrase) {
				t.Fatalf("must not be ErrPassphrase: %v", err)
			}
			if !errors.Is(err, ErrNotBackup) {
				t.Fatalf("want ErrNotBackup, got %v", err)
			}
			requireNoLeakWords(t, err)
		})
	}
}

func TestNewerVersionRejected(t *testing.T) {
	sealed := fixture(t)
	v2 := remap(t, sealed, func(m map[string]any) { m["version"] = 2 })
	_, err := Open(v2, testPass)
	if err == nil {
		t.Fatal("want error")
	}
	if errors.Is(err, ErrPassphrase) {
		t.Fatalf("must not be ErrPassphrase: %v", err)
	}
	if !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("want ErrNewerVersion, got %v", err)
	}
	requireNoLeakWords(t, err)

	// The version check comes before the rest of the header is read.
	v2bad := remap(t, sealed, func(m map[string]any) {
		m["version"] = 2
		m["kdf"] = "something-else"
		delete(m, "salt")
	})
	_, err = Open(v2bad, testPass)
	if !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("newer version with odd header: want ErrNewerVersion, got %v", err)
	}
}

func TestPlaintextNotInSealedFile(t *testing.T) {
	sealed := fixture(t)
	secrets := []string{
		"sk-LEAKKEY-7731",
		"sk-MULTIKEY-9902",
		"HDRVAL-5520",
		"X-Api-Key",
		"team-blue",
		"leak-host-4418.example.test",
		"PROVNAME-3306",
		"PROFILE-LEAK-8812",
		"acme/model-x",
		"PNGDATA-1234",
	}
	for _, s := range secrets {
		if bytes.Contains(sealed, []byte(s)) {
			t.Fatalf("sealed file contains plaintext %q", s)
		}
	}
}

func TestPBKDF2Vectors(t *testing.T) {
	// Known answers for PBKDF2-HMAC-SHA256 with P="password", S="salt".
	cases := map[int]string{
		1: "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b",
		2: "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43",
	}
	for iter, want := range cases {
		got := pbkdf2SHA256([]byte("password"), []byte("salt"), iter, 32)
		if hex.EncodeToString(got) != want {
			t.Fatalf("iter %d: got %x, want %s", iter, got, want)
		}
	}
}

func TestDefaultIterationsAboveFloor(t *testing.T) {
	if defaultIterations <= 100000 {
		t.Fatalf("defaultIterations = %d, must exceed 100000", defaultIterations)
	}
}
