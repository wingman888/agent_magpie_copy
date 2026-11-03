package backup

import (
	"net/url"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestRestoreNoKeyKeepsLocalAddressCredentialsSameOrigin(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{
		ID: "acme", Name: "Acme", Chat: "https://me:s3cret@api.acme.test/v1?api_key=local-q&x=1", Key: "local-key",
	}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "acme", Name: "Acme", Chat: "https://api.acme.test/v2?api_key=stale&team=blue",
	}}}
	if _, err := Restore(b, All); err != nil {
		t.Fatal(err)
	}
	acme, err := provider.Find("acme")
	if err != nil {
		t.Fatal(err)
	}
	if acme.Key != "local-key" {
		t.Errorf("local key lost: %q", acme.Key)
	}
	u, err := url.Parse(acme.Chat)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/v2" {
		t.Errorf("backup path not applied: %q", acme.Chat)
	}
	if u.User == nil || u.User.Username() != "me" {
		t.Fatalf("local userinfo not kept: %q", acme.Chat)
	}
	if pw, _ := u.User.Password(); pw != "s3cret" {
		t.Errorf("local password not kept: %q", acme.Chat)
	}
	q := u.Query()
	if got := q["api_key"]; len(got) != 1 || got[0] != "local-q" {
		t.Errorf("api_key = %v, want the local value once", got)
	}
	if q.Get("team") != "blue" {
		t.Errorf("backup's plain parameter lost: %q", acme.Chat)
	}
	if q.Get("x") != "" {
		t.Errorf("non-credential local parameter should not be carried: %q", acme.Chat)
	}
}

func TestRestoreNoKeyDoesNotKeepLocalAddressCredentialsOtherOrigin(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{
		ID: "acme", Name: "Acme", Chat: "https://me:s3cret@api.acme.test/v1?api_key=local-q", Key: "local-key",
	}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "acme", Name: "Acme", Chat: "https://new.acme.test/v1",
	}}}
	if _, err := Restore(b, All); err != nil {
		t.Fatal(err)
	}
	acme, err := provider.Find("acme")
	if err != nil {
		t.Fatal(err)
	}
	if acme.Chat != "https://new.acme.test/v1" {
		t.Errorf("address changed unexpectedly: %q", acme.Chat)
	}
	if acme.Key != "local-key" {
		t.Errorf("local key lost: %q", acme.Key)
	}
	u, err := url.Parse(acme.Chat)
	if err != nil {
		t.Fatal(err)
	}
	if u.User != nil || u.Query().Get("api_key") != "" {
		t.Errorf("local credentials leaked into another host: %q", acme.Chat)
	}
}
