package db

import "math"

// This file implements the multi-signal identification-confidence model and the
// two "stretch" discriminators (cross-correlation lag, change-point coherence)
// the deep-research pass recommended. Everything here is pure Go over small
// aligned arrays — no heavy ML — so it stays cheap enough to run per candidate.

// confidenceSignals are the per-window inputs to the confidence model. Optional
// signals are pointers; nil means "not measured this window" and is treated
// neutrally rather than as a penalty.
type confidenceSignals struct {
	R, R2         *float64 // Pearson r and regression R² vs monitored energy
	HasMultiplier bool     // a positive slope yielded a calibration
	MeterKwh      float64  // candidate energy over the window at the suggested calibration
	MonitoredKwh  float64  // monitored-subset energy over the window
	FloorOK       *bool    // calibrated low-rate power ≥ monitored floor
	WindowPackets int64    // packets the meter sent in the window
	NBuckets      int      // comparison buckets that fed the regression
	NCandidates   int      // meters with a usable correlation this window (data-snooping)
	ChangePoint   *float64 // 0..1 coherence of the rate step at a known toggle
	LagPenalty    *float64 // 0..1 multiplier from lag plausibility (1 = ideal)
	AnchorAgree   *float64 // 0..1 agreement with a known-load anchor
}

// component weights (sum of the always-present ones ≈ 1 before optional blends).
const (
	wCorr   = 0.45 // correlation strength dominates
	wR2     = 0.10
	wRecon  = 0.15 // energy reconciliation (inequality gate)
	wPkt    = 0.10 // packet/bucket adequacy
	wFloor  = 0.10
	wChange = 0.10 // change-point coherence (neutral 0.5 when absent)
)

// combineConfidence folds the signals into a single 0..1 confidence plus a
// transparent breakdown of each component's contribution. The staged idea from
// the research is encoded here: a sensitive correlation base, then auxiliary
// gates (reconciliation, floor, lag, data-snooping) that strip false positives.
func combineConfidence(s confidenceSignals) (float64, map[string]float64) {
	parts := map[string]float64{}

	corr := 0.0
	if s.R != nil {
		corr = clamp01(*s.R)
	}
	parts["correlation"] = corr

	r2 := corr * corr
	if s.R2 != nil {
		r2 = clamp01(*s.R2)
	}
	parts["r2"] = r2

	// Energy reconciliation as an INEQUALITY: with partial-subset monitoring the
	// candidate's energy should be ≥ the monitored subset's, but not absurdly large.
	recon := 0.5 // neutral when we can't compute it
	if s.HasMultiplier && s.MonitoredKwh > 0 && s.MeterKwh > 0 {
		ratio := s.MeterKwh / s.MonitoredKwh
		switch {
		case ratio < 0.5: // meter reads LESS than a subset it must contain → wrong
			recon = clamp01(ratio / 0.5 * 0.4)
		case ratio <= 1.0: // plausibly equals the monitored subset
			recon = 0.7 + 0.3*((ratio-0.5)/0.5)
		case ratio <= 20.0: // whole home is a few× the monitored subset → ideal
			recon = 1.0
		case ratio <= 100.0: // large but not impossible
			recon = 1.0 - 0.5*((ratio-20.0)/80.0)
		default: // implausible scale → likely a miscalibration/false match
			recon = 0.3
		}
	}
	parts["reconciliation"] = recon

	// Packet/bucket adequacy: thin data can't support a confident call.
	pkt := math.Min(1, float64(s.NBuckets)/30.0)
	if s.WindowPackets > 0 {
		pkt = math.Min(pkt, math.Min(1, float64(s.WindowPackets)/60.0))
	}
	parts["packets"] = pkt

	floor := 1.0
	if s.FloorOK != nil && !*s.FloorOK {
		floor = 0.4 // soft: a meter below the monitored floor is unlikely whole-home
	}
	parts["floor"] = floor

	change := 0.5 // neutral when there's no known toggle to test against
	if s.ChangePoint != nil {
		change = clamp01(*s.ChangePoint)
	}
	parts["change_point"] = change

	base := wCorr*corr + wR2*r2 + wRecon*recon + wPkt*pkt + wFloor*floor + wChange*change

	// A known-load anchor is the strongest evidence — blend it in heavily.
	if s.AnchorAgree != nil {
		a := clamp01(*s.AnchorAgree)
		parts["anchor"] = a
		base = 0.45*base + 0.55*a
	}

	// Lag plausibility and data-snooping apply as multiplicative gates.
	lag := 1.0
	if s.LagPenalty != nil {
		lag = clamp01(*s.LagPenalty)
	}
	parts["lag"] = lag

	snoop := snoopFactor(s.R, s.NBuckets, s.NCandidates)
	parts["snoop"] = snoop

	conf := clamp01(base * lag * snoop)
	parts["confidence"] = round(conf, 3)
	return conf, parts
}

// snoopFactor down-weights confidence when a correlation could plausibly be a
// coincidence among many candidates. It compares |r| against a Bonferroni-
// corrected critical correlation r* = z(1 − 0.05/k) / sqrt(n), smoothly ramping
// from 0 at 0.7·r* to 1 at 1.3·r*. With few candidates / strong r it returns 1.
func snoopFactor(r *float64, nBuckets, nCandidates int) float64 {
	if r == nil || nBuckets < 4 {
		return 1
	}
	k := nCandidates
	if k < 1 {
		k = 1
	}
	p := 0.05 / float64(k)
	z := math.Sqrt2 * math.Erfinv(1-p) // two-sided critical z
	rStar := z / math.Sqrt(float64(nBuckets))
	if rStar > 0.95 {
		rStar = 0.95
	}
	return smoothstep(0.7*rStar, 1.3*rStar, math.Abs(*r))
}

// crossCorrLag returns the integer lag (in buckets, within ±maxLag) at which the
// meter-delta series best correlates with the reference-energy series, plus the
// correlation at that lag. A true meter shows a small, consistent lag (its
// reporting delay); a spurious match peaks at an implausible lag or barely beats
// zero-lag. Both series must be index-aligned and equal length.
func crossCorrLag(meter, ref []float64, maxLag int) (int, float64) {
	n := len(meter)
	if n != len(ref) || n < 4 {
		return 0, 0
	}
	bestLag, bestR := 0, math.Inf(-1)
	for lag := -maxLag; lag <= maxLag; lag++ {
		var a, b []float64
		if lag >= 0 {
			a, b = meter[lag:], ref[:n-lag]
		} else {
			a, b = meter[:n+lag], ref[-lag:]
		}
		r := pearson(a, b)
		if r > bestR {
			bestR, bestLag = r, lag
		}
	}
	if math.IsInf(bestR, -1) {
		return 0, 0
	}
	return bestLag, bestR
}

// lagPenalty converts a best lag (buckets) into a 0..1 plausibility multiplier:
// zero/small lags are ideal; larger reporting delays are progressively penalized
// but never zeroed (a real meter can lag a couple of buckets).
func lagPenalty(lag, maxLag int) float64 {
	if maxLag <= 0 {
		return 1
	}
	a := math.Abs(float64(lag)) / float64(maxLag)
	return clamp01(1 - 0.5*a)
}

// changePointCoherence scores whether the candidate's consumption rate steps up
// during the toggle window relative to the surrounding baseline — the signature
// of the meter that actually saw the load. It is a simple CUSUM-style rate-ratio,
// not one of the (refuted) heavy change-point ensembles. Returns 0..1.
func changePointCoherence(windowRate, baselineRate float64) float64 {
	if windowRate <= 0 {
		return 0
	}
	if baselineRate <= 0 {
		return 1 // any activity against a silent baseline is a clean step
	}
	ratio := windowRate / baselineRate
	// ratio 1 → no step (0.5); ratio ≥ 3 → a clear step (→1); below 1 → suppressed.
	return clamp01(0.5 + 0.25*(ratio-1))
}

func pearson(a, b []float64) float64 {
	n := len(a)
	if n != len(b) || n < 2 {
		return 0
	}
	var sa, sb float64
	for i := range a {
		sa += a[i]
		sb += b[i]
	}
	ma, mb := sa/float64(n), sb/float64(n)
	var sab, saa, sbb float64
	for i := range a {
		da, db := a[i]-ma, b[i]-mb
		sab += da * db
		saa += da * da
		sbb += db * db
	}
	if saa == 0 || sbb == 0 {
		return 0
	}
	return sab / math.Sqrt(saa*sbb)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func smoothstep(lo, hi, x float64) float64 {
	if hi <= lo {
		if x >= hi {
			return 1
		}
		return 0
	}
	t := clamp01((x - lo) / (hi - lo))
	return t * t * (3 - 2*t)
}
