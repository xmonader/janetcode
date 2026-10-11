package config

import (
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

type fixedRates struct{ p *llm.Price }

func (r fixedRates) PriceFor(string) *llm.Price { return r.p }

// A provider listing "pricing": {"prompt":"0","completion":"0"} (a broker
// serving a self-hosted model) has not told us the model is free; it has
// told us nothing. /connect saved that as a known zero price, and the
// status line then read "$0.00 est." for a session that may well cost
// money. An all-zero listed price is unknown.
func TestListedZeroPriceIsUnknown(t *testing.T) {
	meta := MetaOf(llm.ModelInfo{ID: "m", ContextWindow: 1000, Price: &llm.Price{}})
	if meta.Price != nil {
		t.Fatalf("MetaOf kept a zero price: %+v", *meta.Price)
	}
	if got := MetaOf(llm.ModelInfo{ID: "m", Price: &llm.Price{Input: 1, Output: 2}}); got.Price == nil || got.Price.Output != 2 {
		t.Fatalf("MetaOf lost a real price: %+v", got.Price)
	}

	// Config files written before the fix carry the zero record.
	c := &Config{Models: map[string]ModelMeta{"m": {ContextWindow: 1000, Price: &llm.Price{}}}}
	if p := c.PriceFor("m"); p != nil {
		t.Fatalf("a stored zero listing price reads as known: %+v", *p)
	}
	if _, ok := (llm.Usage{InputTokens: 1000, OutputTokens: 500}).Cost(c.PriceFor("m")); ok {
		t.Fatal("cost reads as a known $0")
	}
	// The rate table still gets its say.
	c.RatesTable = fixedRates{&llm.Price{Input: 3}}
	if p := c.PriceFor("m"); p == nil || p.Input != 3 {
		t.Fatalf("rate table not consulted: %+v", p)
	}
	// A zero the user wrote into "prices" is a deliberate statement.
	c.Prices = map[string]llm.Price{"m": {}}
	if p := c.PriceFor("m"); p == nil {
		t.Fatal("an explicit zero price was dropped")
	}
}
