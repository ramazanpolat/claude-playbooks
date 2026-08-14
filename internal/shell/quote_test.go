package shell

import "testing"

func TestQuoteArg(t *testing.T) {
	if got := QuoteArg(`a b; touch PWNED`); got != `'a b; touch PWNED'` {
		t.Fatalf("QuoteArg = %s", got)
	}
	if got := QuoteArg(`bob's`); got != `'bob'\''s'` {
		t.Fatalf("QuoteArg = %s", got)
	}
}
