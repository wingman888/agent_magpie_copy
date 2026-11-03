package gateway

import "testing"

// A 400 moves on only when it says the model is gone or the account is out.
// A parameter that is wrong, the request's size, or a missing field is the
// request's own fault, whatever words the vendor uses for it.
func TestBadRequestMovesOnOnlyForAGoneModel(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`messages: field required`, false},
		{`prompt tokens exceeded maximum tokens`, false},
		{`maximum tokens 4096 exceeded`, false},
		{`invalid model_reasoning_effort: xhigh`, false},
		{`unsupported parameter: model_reasoning_effort for gpt-x`, false},
		{`模型不存在`, true},
		{`Invalid model: gpt-x`, true},
		{`unsupported model gpt-x`, true},
	} {
		if got := retryable(400, []byte(tc.body)); got != tc.want {
			t.Errorf("retryable(400, %q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}
