package backup

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const (
	testKey    = "sk-test-SECRET-0123456789"
	testKey2   = "sk-test-SECOND-abcdefghij"
	testHost   = "api.vendor-example.test"
	testName   = "Vendor Example"
	testPass   = "correct horse battery"
	testHeader = "X-Private-Gateway-Token"
)

func sampleBundle() Bundle {
	return Bundle{
		Version: 1,
		Providers: []provider.Provider{{
			ID:      "vendor",
			Name:    testName,
			Key:     testKey,
			Chat:    "https://" + testHost + "/v1",
			Headers: map[string]string{testHeader: "hdr-secret-value", "X-Team": "platform"},
			Keys: []provider.KeyAccount{
				{Name: "second", Key: testKey2},
			},
		}},
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	data, err := Seal(sampleBundle(), testPass)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := Open(data, testPass)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1", got.Version)
	}
	if len(got.Providers) != 1 {
		t.Fatalf("Providers = %d, want 1", len(got.Providers))
	}
	p := got.Providers[0]
	if p.ID != "vendor" || p.Name != testName || p.Key != testKey || p.Chat != "https://"+testHost+"/v1" {
		t.Errorf("provider fields not preserved: %+v", p)
	}
	if p.Headers[testHeader] != "hdr-secret-value" || p.Headers["X-Team"] != "platform" {
		t.Errorf("headers not preserved: %v", p.Headers)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != testKey2 || p.Keys[0].Name != "second" {
		t.Errorf("multi-key accounts not preserved: %+v", p.Keys)
	}
}

func TestWrongPassphrase(t *testing.T) {
	data, err := Seal(sampleBundle(), testPass)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	_, err = Open(data, "not the passphrase")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("Open with wrong passphrase: err = %v, want ErrPassphrase", err)
	}
}

func TestEmptyPassphrase(t *testing.T) {
	if _, err := Seal(sampleBundle(), ""); err == nil {
		t.Error("Seal with empty passphrase: want error")
	}
	data, err := Seal(sampleBundle(), testPass)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(data, ""); err == nil {
		t.Error("Open with empty passphrase: want error")
	}
}

func TestNotABackup(t *testing.T) {
	cases := map[string][]byte{
		"empty":   {},
		"blank":   []byte("  \n"),
		"hello":   []byte("hello"),
		"object":  []byte("{}"),
		"other":   []byte(`{"format":"something-else","version":1}`),
		"numeric": []byte("42"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Open(data, testPass)
			if err == nil {
				t.Fatal("want error")
			}
			if errors.Is(err, ErrPassphrase) {
				t.Errorf("err = %v; must not be ErrPassphrase", err)
			}
			assertNoPassWord(t, err)
			if !strings.Contains(err.Error(), "不是 magpie 备份") {
				t.Errorf("err = %q, want a not-a-backup message", err)
			}
		})
	}
}

func TestNewerVersion(t *testing.T) {
	// Only the header matters for this check; no kdf or other fields are needed.
	data := []byte(`{"format":"magpie-backup","version":2}` + "\nAAAA\n")
	_, err := Open(data, testPass)
	if err == nil {
		t.Fatal("want error")
	}
	if errors.Is(err, ErrPassphrase) {
		t.Errorf("err = %v; must not be ErrPassphrase", err)
	}
	assertNoPassWord(t, err)
	if !strings.Contains(err.Error(), "升级") {
		t.Errorf("err = %q, want an upgrade hint", err)
	}
}

func TestIterationsTampered(t *testing.T) {
	data, err := Seal(sampleBundle(), testPass)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !bytes.Contains(data, []byte(`"iterations":600000`)) {
		t.Fatalf("default iterations not written as a JSON integer:\n%s", data)
	}
	weak := bytes.Replace(data, []byte(`"iterations":600000`), []byte(`"iterations":100000`), 1)
	_, err = Open(weak, testPass)
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("Open with iterations lowered to 100000: err = %v, want ErrPassphrase", err)
	}
}

func TestSaltTampered(t *testing.T) {
	data, err := Seal(sampleBundle(), testPass)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	head := data[:bytes.IndexByte(data, '\n')]
	// Flip one character inside the salt value; the header is authenticated.
	i := bytes.Index(head, []byte(`"salt":"`)) + len(`"salt":"`)
	bad := append([]byte(nil), data...)
	if bad[i] == 'A' {
		bad[i] = 'B'
	} else {
		bad[i] = 'A'
	}
	if _, err := Open(bad, testPass); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("Open with tampered salt: err = %v, want ErrPassphrase", err)
	}
}

func TestNoPlaintextInFile(t *testing.T) {
	data, err := Seal(sampleBundle(), testPass)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	for _, s := range []string{testKey, testKey2, testHost, testName, testHeader, "hdr-secret-value", "vendor"} {
		if bytes.Contains(data, []byte(s)) {
			t.Errorf("sealed file contains plaintext %q", s)
		}
	}
}

func assertNoPassWord(t *testing.T, err error) {
	t.Helper()
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "passphrase") || strings.Contains(err.Error(), "口令") {
		t.Errorf("error %q must not mention the passphrase", err)
	}
}
