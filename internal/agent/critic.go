package agent

import (
	"github.com/xmonader/janetcode/internal/critic"
	"github.com/xmonader/janetcode/internal/session"
)

// The fitted trajectory model and /why live in internal/critic: both are
// arithmetic and lookups over what the harness already wrote down, with
// no agent and no model call. Folding a live turn into features stays
// here (see trajectory.go), because it reads the turn's own loop
// signatures. These keep the names the front-ends, eval and cmd use.

// FeatureNames is the trajectory feature order; see critic.FeatureNames.
var FeatureNames = critic.FeatureNames

// Sample is one trajectory with its outcome, for training.
type Sample = critic.Sample

// Predictor is a logistic model over standardised trajectory features.
type Predictor = critic.Predictor

// ErrOneClass is returned by Train when every sample has the same outcome.
var ErrOneClass = critic.ErrOneClass

// Train fits a predictor from eval samples; see critic.Train.
func Train(samples []Sample) (*Predictor, error) { return critic.Train(samples) }

// LoadPredictor reads a predictor saved by Predictor.Save.
func LoadPredictor(path string) (*Predictor, error) { return critic.LoadPredictor(path) }

// InterventionKind names what sort of thing the harness did.
type InterventionKind = critic.InterventionKind

// Intervention is one thing the harness did, read back off the ledger.
type Intervention = critic.Intervention

// Interventions reads the session ledger for what the harness did.
func Interventions(events []session.Event) []Intervention { return critic.Interventions(events) }

// WhyLast explains the n-th most recent intervention in plain words.
func WhyLast(events []session.Event, n int) string { return critic.WhyLast(events, n) }
