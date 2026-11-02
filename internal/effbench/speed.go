package effbench

// Totals is what the proxy log says about one run.
type Totals struct {
	Calls, Input, Cached, Fresh, Output int
	Errors                              int // non-2xx
	ModelMS, DecodeMS                   int64
	Asked, Served                       []string // distinct, in first-seen order
	Mismatch                            bool
}

// Sum folds records into totals.
func Sum(recs []Record) Totals {
	var t Totals
	for _, r := range recs {
		t.Calls++
		t.Input += r.Input
		t.Cached += r.Cached
		t.Fresh += r.Fresh
		t.Output += r.Output
		t.ModelMS += r.ElapsedMS
		t.DecodeMS += r.DecodeMS
		if r.Status < 200 || r.Status > 299 {
			t.Errors++
		}
		t.Asked = addUniq(t.Asked, r.Asked)
		t.Served = addUniq(t.Served, r.Served)
		t.Mismatch = t.Mismatch || r.Mismatch
	}
	return t
}

func addUniq(xs []string, s string) []string {
	if s == "" {
		return xs
	}
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}

// TPS is the run's decode speed in output tokens per second (0 if unknown).
func (t Totals) TPS() float64 {
	if t.DecodeMS <= 0 || t.Output <= 0 {
		return 0
	}
	return float64(t.Output) / (float64(t.DecodeMS) / 1000)
}

// AdjustedSeconds recomputes wall time as if the model had decoded at refTPS:
// wall - decode time + output / refTPS. Runs from days with a slow or fast
// broker then compare on what the agent did, not on the broker's mood. With
// no usable speed on either side the wall time is returned unchanged.
func AdjustedSeconds(wallSec float64, t Totals, refTPS float64) float64 {
	own := t.TPS()
	if own <= 0 || refTPS <= 0 {
		return wallSec
	}
	decode := float64(t.DecodeMS) / 1000
	return wallSec - decode + float64(t.Output)/refTPS
}
