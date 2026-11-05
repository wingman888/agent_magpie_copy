package provider

// What is left on an API key, as the vendor's own balance endpoint tells
// it: DeepSeek, Kimi, OpenRouter and SiliconFlow are known by their hosts;
// any other provider can name an endpoint and where the amount sits in its
// reply (BalanceURL, BalancePath), the way a relay's own usage query does.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// balanceSource is where one provider's balance is asked and how the
// reply reads.
type balanceSource struct {
	url  string
	read func(body []byte) (string, error)
}

// balanceSourceOf is the provider's own endpoint when it named one, else
// the one its host is known to have.
func balanceSourceOf(p Provider) (balanceSource, bool) {
	if p.BalanceURL != "" {
		path := p.BalancePath
		return balanceSource{p.BalanceURL, func(b []byte) (string, error) { return readBalancePath(b, path) }}, true
	}
	hosts := []string{hostOf(p.Chat), hostOf(p.Responses), hostOf(p.Anthropic)}
	for _, h := range hosts {
		switch h {
		case "api.deepseek.com":
			return balanceSource{"https://api.deepseek.com/user/balance", readDeepSeek}, true
		case "api.moonshot.cn":
			return balanceSource{"https://api.moonshot.cn/v1/users/me/balance", readMoonshot("¥")}, true
		case "api.moonshot.ai":
			return balanceSource{"https://api.moonshot.ai/v1/users/me/balance", readMoonshot("$")}, true
		case "openrouter.ai":
			return balanceSource{"https://openrouter.ai/api/v1/credits", readOpenRouter}, true
		case "api.siliconflow.cn":
			return balanceSource{"https://api.siliconflow.cn/v1/user/info", readSiliconFlow("¥")}, true
		case "api.siliconflow.com":
			return balanceSource{"https://api.siliconflow.com/v1/user/info", readSiliconFlow("$")}, true
		}
	}
	return balanceSource{}, false
}

// money is an amount with its currency's sign in front: "¥12.34".
func money(sign string, v float64) string {
	return sign + strconv.FormatFloat(v, 'f', 2, 64)
}

func currencySign(code string) string {
	switch strings.ToUpper(code) {
	case "CNY", "RMB":
		return "¥"
	case "USD":
		return "$"
	case "":
		return ""
	}
	return strings.ToUpper(code) + " "
}

// number reads an amount given as a JSON number or as a string of one.
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// readDeepSeek: {"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.00",…}]}
func readDeepSeek(b []byte) (string, error) {
	var r struct {
		Infos []struct {
			Currency string `json:"currency"`
			Total    any    `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", err
	}
	var parts []string
	for _, in := range r.Infos {
		if v, ok := number(in.Total); ok {
			parts = append(parts, money(currencySign(in.Currency), v))
		}
	}
	if len(parts) == 0 {
		return "", errors.New("no balance in the reply")
	}
	return strings.Join(parts, " · "), nil
}

// readMoonshot: {"code":0,"data":{"available_balance":49.58,…},"status":true}
func readMoonshot(sign string) func([]byte) (string, error) {
	return func(b []byte) (string, error) {
		var r struct {
			Data struct {
				Available any `json:"available_balance"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return "", err
		}
		v, ok := number(r.Data.Available)
		if !ok {
			return "", errors.New("no balance in the reply")
		}
		return money(sign, v), nil
	}
}

// readOpenRouter: {"data":{"total_credits":20,"total_usage":3.5}}, in dollars.
func readOpenRouter(b []byte) (string, error) {
	var r struct {
		Data struct {
			Credits any `json:"total_credits"`
			Usage   any `json:"total_usage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", err
	}
	credits, ok := number(r.Data.Credits)
	if !ok {
		return "", errors.New("no credits in the reply")
	}
	used, _ := number(r.Data.Usage)
	return money("$", credits-used), nil
}

// readSiliconFlow: {"code":20000,"data":{"totalBalance":"88.88",…}}
func readSiliconFlow(sign string) func([]byte) (string, error) {
	return func(b []byte) (string, error) {
		var r struct {
			Data struct {
				Total any `json:"totalBalance"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return "", err
		}
		v, ok := number(r.Data.Total)
		if !ok {
			return "", errors.New("no balance in the reply")
		}
		return money(sign, v), nil
	}
}

// readBalancePath picks the amount out of a reply by a dotted path, array
// items by their index: "data.total_available", "balance_infos.0.total_balance".
// A "/ n" after the path divides by n, for a relay counting in its own
// units ("data.total_available / 500000"); a "$" or "¥" before it is put
// in front of the amount. A value that is not a number is shown as it is.
func readBalancePath(b []byte, path string) (string, error) {
	path = strings.TrimSpace(path)
	sign := ""
	for _, s := range []string{"$", "¥", "€", "£"} {
		if rest, ok := strings.CutPrefix(path, s); ok {
			sign, path = s, strings.TrimSpace(rest)
			break
		}
	}
	div := 1.0
	if p, d, ok := strings.Cut(path, "/"); ok {
		n, err := strconv.ParseFloat(strings.TrimSpace(d), 64)
		if err != nil || n == 0 {
			return "", fmt.Errorf("the balance path divides by %q, not a number", strings.TrimSpace(d))
		}
		path, div = strings.TrimSpace(p), n
	}
	if path == "" {
		return "", errors.New("no balance path: where in the reply the amount is, e.g. data.balance")
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "", errors.New("the reply is not JSON")
	}
	for _, k := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[k]
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(x) {
				return "", fmt.Errorf("nothing at %q in the reply", path)
			}
			v = x[i]
		default:
			v = nil
		}
		if v == nil {
			return "", fmt.Errorf("nothing at %q in the reply", path)
		}
	}
	if n, ok := number(v); ok {
		return money(sign, n/div), nil
	}
	if s, ok := v.(string); ok && s != "" {
		return sign + s, nil
	}
	return "", fmt.Errorf("%q in the reply is not an amount", path)
}

// Balance asks the vendor what is left on the provider's key in use. ok is
// false when there is no way to ask it.
func Balance(ctx context.Context, p Provider) (amount string, ok bool, err error) {
	src, ok := balanceSourceOf(p)
	if !ok || p.Account != nil || p.Key == "" {
		return "", false, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.url, nil)
	if err != nil {
		return "", true, err
	}
	for k, v := range AuthHeaders(p, Chat) {
		req.Header.Set(k, v)
	}
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", true, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		// what a JSON reply says, on one line; a page of HTML says nothing
		msg := strings.Join(strings.Fields(string(b)), " ")
		if !strings.HasPrefix(msg, "{") {
			return "", true, errors.New(res.Status)
		}
		if r := []rune(msg); len(r) > 200 {
			msg = string(r[:200]) + "…"
		}
		return "", true, fmt.Errorf("%s: %s", res.Status, msg)
	}
	amount, err = src.read(b)
	return amount, true, err
}

var keyBalanceCache struct {
	sync.Mutex
	at   time.Time
	data []SubscriptionQuota
}

// KeyBalances is the balance of every provider magpie can ask one of, the
// key in use and each other key it has on, as cards beside the
// subscriptions' allowances. What was asked less than a minute ago is not
// asked again.
func KeyBalances(ctx context.Context) []SubscriptionQuota {
	c := &keyBalanceCache
	c.Lock()
	if c.data != nil && time.Since(c.at) < time.Minute {
		defer c.Unlock()
		return c.data
	}
	c.Unlock()
	type job struct {
		p    Provider
		user string
	}
	var jobs []job
	for _, p := range All() {
		if p.Hidden || p.Account != nil || p.Key == "" {
			continue
		}
		if _, ok := balanceSourceOf(p); !ok {
			continue
		}
		others := 0
		for _, k := range p.Keys {
			if !k.Off && k.Key != "" && k.Key != p.Key {
				others++
			}
		}
		if others == 0 {
			jobs = append(jobs, job{p, ""})
			continue
		}
		first := p.KeyName
		if first == "" {
			first = Mask(p.Key)
		}
		jobs = append(jobs, job{p, first})
		for _, k := range p.Keys {
			if k.Off || k.Key == "" || k.Key == p.Key {
				continue
			}
			q := p
			q.Key = k.Key
			name := k.Name
			if name == "" {
				name = Mask(k.Key)
			}
			jobs = append(jobs, job{q, name})
		}
	}
	out := make([]SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := SubscriptionQuota{Provider: j.p.ID, Name: j.p.Name, Icon: j.p.Icon, User: j.user, Windows: []QuotaWindow{}}
			amount, _, err := Balance(ctx, j.p)
			if err != nil {
				q.Error = err.Error()
			} else {
				q.Balance = amount
			}
			out[i] = q
		}()
	}
	wg.Wait()
	if ctx.Err() == nil {
		c.Lock()
		c.at, c.data = time.Now(), out
		c.Unlock()
	}
	return out
}
