package backup

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestCheckFormatWithoutPassphrase(t *testing.T) {
	for _, in := range []string{"", "hello", "{}", `{"format":"other"}`} {
		err := CheckFormat([]byte(in))
		if err == nil {
			t.Errorf("%q: accepted", in)
			continue
		}
		if errors.Is(err, ErrPassphrase) || !strings.Contains(err.Error(), "不是 magpie 备份") {
			t.Errorf("%q: err = %v, want the not-a-backup message", in, err)
		}
	}

	data, err := Seal(Bundle{Version: 1}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckFormat(data); err != nil {
		t.Fatalf("valid backup refused: %v", err)
	}

	newer := bytes.Replace(data, []byte(`"version": 1`), []byte(`"version": 2`), 1)
	err = CheckFormat(newer)
	if err == nil || errors.Is(err, ErrPassphrase) || !strings.Contains(err.Error(), "升级") {
		t.Errorf("newer backup: err = %v, want the upgrade message", err)
	}

	// CheckFormat does not look at the rest of the header.
	bad := bytes.Replace(data, []byte(`"iterations": 600000`), []byte(`"iterations": 100000`), 1)
	if err := CheckFormat(bad); err != nil {
		t.Errorf("CheckFormat looked past the format and version: %v", err)
	}
}

func TestHeaderFieldWrongTypeIsPassphraseError(t *testing.T) {
	data, err := Seal(Bundle{Version: 1}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ from, to string }{
		{`"iterations": 600000`, `"iterations": "600000"`},
		{`"version": 1`, `"version": "1"`},
		{`"salt": "`, `"salt": 7, "x": "`},
	} {
		bad := bytes.Replace(data, []byte(c.from), []byte(c.to), 1)
		if bytes.Equal(bad, data) {
			t.Fatalf("test did not change the header: %s", c.from)
		}
		_, err := Open(bad, "pw")
		if !errors.Is(err, ErrPassphrase) {
			t.Errorf("%s: err = %v, want ErrPassphrase", c.to, err)
		}
	}
}

func TestNoKeysDropsURLCredentials(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{
		ID: "link", Name: "Link", Chat: "https://user:pw-secret@api.link.test/v1?api_key=url-secret&model=m1",
		Anthropic: "https://api.link.test?token=tok-secret",
	}); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	p := b.Providers[0]
	if p.Chat != "https://api.link.test/v1?model=m1" {
		t.Errorf("chat = %q", p.Chat)
	}
	if p.Anthropic != "https://api.link.test" {
		t.Errorf("anthropic = %q", p.Anthropic)
	}
	data, err := Seal(b, "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"pw-secret", "url-secret", "tok-secret"} {
		if bytes.Contains(data, []byte(s)) {
			t.Errorf("backup contains %q", s)
		}
	}
	// With keys, the address is kept as typed.
	full, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if full.Providers[0].Chat != "https://user:pw-secret@api.link.test/v1?api_key=url-secret&model=m1" {
		t.Errorf("keyed backup changed the address: %q", full.Providers[0].Chat)
	}
}

func TestRestoreNoKeyKeepsLocalCredentialHeaders(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{
		ID: "acme", Name: "Acme", Chat: "https://api.acme.test/v1", Key: "local-key",
		Headers: map[string]string{"X-Api-Key": "local-hdr-secret", "X-Old": "old"},
	}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "acme", Name: "Acme", Chat: "https://new.acme.test/v1",
		Headers: map[string]string{"X-Team": "blue"},
	}}}
	if _, err := Restore(b, All); err != nil {
		t.Fatal(err)
	}
	acme, err := provider.Find("acme")
	if err != nil {
		t.Fatal(err)
	}
	if acme.Key != "local-key" || acme.Chat != "https://new.acme.test/v1" {
		t.Errorf("acme = %+v", acme)
	}
	if acme.Headers["X-Api-Key"] != "local-hdr-secret" {
		t.Errorf("local credential header lost: %v", acme.Headers)
	}
	if acme.Headers["X-Team"] != "blue" {
		t.Errorf("backup's plain header not applied: %v", acme.Headers)
	}
}
