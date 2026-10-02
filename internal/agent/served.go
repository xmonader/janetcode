package agent

import (
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/provenance"
)

// checkServed compares what a response said about who served it with
// the model that was asked for (internal/provenance), counts it, and says
// so once per session for each requested model and served vendor: a
// gateway that answers a model name with another vendor's model gives no
// sign of it in the answer, and every number measured afterwards belongs
// to the other model.
func (a *Agent) checkServed(model string, resp *llm.Response, emit func(Event)) {
	if resp == nil {
		return
	}
	v := provenance.Check(model, resp.Served)
	a.lastServed = resp.Served
	a.lastMismatch = ""
	if v.Mismatch {
		a.lastMismatch = v.String()
	}
	if a.stats.served(model, resp.Served, v) && emit != nil {
		emit(Event{Kind: EvNotice, Text: "requested " + model + ", but the response was " + v.String() + "; results from this session are that model's, not " + model + "'s"})
	}
}

// servedForLog is the provenance to write on the assistant event of the
// call just made: the provenance when it changed since the last one
// written, or when it is a mismatch; nil otherwise, so a long session on
// one honest route carries it once.
func (a *Agent) servedForLog() (*llm.Provenance, string) {
	p := a.lastServed
	if p.Empty() {
		return nil, ""
	}
	if p == a.loggedServed && a.lastMismatch == "" {
		return nil, ""
	}
	a.loggedServed = p
	return &p, a.lastMismatch
}
