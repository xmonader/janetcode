package session

import "github.com/xmonader/janetcode/internal/llm"

// TokenNode is one current-surface message priced in tokens.
type TokenNode struct {
	Seq    int
	Tokens int
	Kind   string
}

// TokenMeasurement is the current request surface at one log revision.
type TokenMeasurement struct {
	LogRevision int
	Surface     int
	Nodes       []TokenNode
}

// EstimateTokens is the caller-supplied sizer (usually Agent.estimate of one message).
type EstimateTokens func(llm.Message) int

// Measure prices current surface events. Shadowed nodes are omitted.
func Measure(events []Event, est EstimateTokens) TokenMeasurement {
	hidden := shadowedSet(events)
	m := TokenMeasurement{LogRevision: 0}
	if n := len(events); n > 0 {
		m.LogRevision = events[n-1].Seq
	}
	for _, e := range events {
		if !e.Surface || e.Message == nil || hidden[e.Seq] {
			continue
		}
		tok := 0
		if est != nil {
			tok = est(*e.Message)
		} else if e.Tokens > 0 {
			tok = e.Tokens
		} else {
			tok = len(e.Message.Text()) / 4
		}
		m.Nodes = append(m.Nodes, TokenNode{Seq: e.Seq, Tokens: tok, Kind: e.Type})
		m.Surface += tok
	}
	return m
}
