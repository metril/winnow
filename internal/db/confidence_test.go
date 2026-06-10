package db

import (
	"math"
	"testing"
)

func TestCounterModulus(t *testing.T) {
	if counterModulus("SCM") != 16777216 {
		t.Fatalf("SCM modulus should be 2^24")
	}
	for _, mt := range []string{"SCM+", "IDM", "NetIDM", "weird", ""} {
		if counterModulus(mt) != 4294967296 {
			t.Fatalf("%q modulus should default to 2^32", mt)
		}
	}
}

func TestDemingSlopeDebiasesOLS(t *testing.T) {
	// y = 6x exactly, but x is observed with noise. OLS regresses y on the noisy x
	// and attenuates (slope < 6); Deming, accounting for error in both, stays closer.
	const trueSlope = 6.0
	xs := make([]float64, 0, 200)
	ys := make([]float64, 0, 200)
	// deterministic pseudo-noise so the test is stable (no Math.random in this env).
	for i := 0; i < 200; i++ {
		xt := float64(i%50) + 1
		nx := math.Sin(float64(i)*1.3) * 3 // measurement noise on x
		xs = append(xs, xt+nx)
		ys = append(ys, trueSlope*xt) // y from the TRUE x
	}
	sxx, syy, sxy := sums(xs, ys)
	ols := sxy / sxx
	dem := demingSlope(sxx, syy, sxy)
	if !(dem > ols) {
		t.Fatalf("Deming (%v) should exceed the attenuated OLS slope (%v)", dem, ols)
	}
	if math.Abs(dem-trueSlope) >= math.Abs(ols-trueSlope) {
		t.Fatalf("Deming should be closer to %v: deming=%v ols=%v", trueSlope, dem, ols)
	}
}

// sums returns regr_sxx/syy/sxy with x as the independent variable, matching the
// SQL ordering used in CorrelationVsReference.
func sums(xs, ys []float64) (sxx, syy, sxy float64) {
	n := float64(len(xs))
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx, my = mx/n, my/n
	for i := range xs {
		dx, dy := xs[i]-mx, ys[i]-my
		sxx += dx * dx
		syy += dy * dy
		sxy += dx * dy
	}
	return
}

func TestCrossCorrLagRecoversShift(t *testing.T) {
	// ref is a square-ish wave; meter is the same wave delayed by 2 buckets.
	n := 40
	ref := make([]float64, n)
	meter := make([]float64, n)
	for i := 0; i < n; i++ {
		ref[i] = math.Sin(float64(i) * 0.7)
	}
	lagTrue := 2
	for i := 0; i < n; i++ {
		j := i - lagTrue
		if j >= 0 {
			meter[i] = ref[j]
		}
	}
	lag, r := crossCorrLag(meter, ref, 5)
	if lag != lagTrue {
		t.Fatalf("expected lag %d, got %d (r=%v)", lagTrue, lag, r)
	}
	if lagPenalty(0, 5) <= lagPenalty(5, 5) {
		t.Fatal("zero lag should be more plausible than max lag")
	}
}

func TestChangePointCoherence(t *testing.T) {
	if changePointCoherence(0, 10) != 0 {
		t.Fatal("no in-window activity should score 0")
	}
	if changePointCoherence(50, 0) != 1 {
		t.Fatal("activity against a silent baseline should score 1")
	}
	flat := changePointCoherence(10, 10) // no step
	step := changePointCoherence(40, 10) // 4x step
	if !(step > flat) {
		t.Fatalf("a clear step (%v) should beat no step (%v)", step, flat)
	}
}

func TestSnoopFactorPenalizesManyCandidates(t *testing.T) {
	r := 0.4
	few := snoopFactor(&r, 150, 2)
	many := snoopFactor(&r, 150, 500)
	if !(many < few) {
		t.Fatalf("a modest r among many candidates should be penalized more: few=%v many=%v", few, many)
	}
	strong := 0.95
	if snoopFactor(&strong, 150, 500) < 0.99 {
		t.Fatal("a strong r should survive the data-snooping correction")
	}
}

func TestCombineConfidencePhysics(t *testing.T) {
	weak := 0.15
	base := confidenceSignals{
		R: &weak, HasMultiplier: true, MeterKwh: 35, MonitoredKwh: 30,
		WindowPackets: 500, NBuckets: 100, NCandidates: 4,
	}
	plain, _ := combineConfidence(base)

	pass := base
	score := 0.9
	pass.Physics = &score
	pc, parts := combineConfidence(pass)
	if !(pc > plain) {
		t.Fatalf("a physics pass should lift a weak-correlation candidate: %v vs %v", pc, plain)
	}
	if parts["physics"] != 0.9 {
		t.Fatalf("physics part missing from breakdown: %v", parts)
	}
	// the blend lands after lag/snoop, so even a snoop-crushed candidate keeps
	// roughly half its physics evidence.
	if pc < 0.4 {
		t.Fatalf("physics-passing candidate should keep substantial confidence, got %v", pc)
	}

	viol := base
	viol.PhysicsViolation = true
	vc, vparts := combineConfidence(viol)
	if !(vc < 0.2*plain+1e-9) {
		t.Fatalf("a physics violation must gate hard: %v vs plain %v", vc, plain)
	}
	if vparts["physics"] != 0 {
		t.Fatalf("violation should zero the physics part: %v", vparts)
	}
}

func TestCombineConfidenceFlatReference(t *testing.T) {
	// Modest correlation but perfect energy reconciliation — the situation of a
	// server-dominated home. With a flat reference (low CV) the correlation weight
	// shifts onto reconciliation, so the candidate scores higher than under the
	// default weighting. (r is chosen to clear the snoop gate, which multiplies
	// both sides equally and would otherwise mask the reweight.)
	r := 0.4
	sig := confidenceSignals{
		R: &r, HasMultiplier: true, MeterKwh: 35, MonitoredKwh: 30,
		WindowPackets: 500, NBuckets: 144, NCandidates: 4,
	}
	plain, _ := combineConfidence(sig)
	flat := sig
	cv := 0.05
	flat.RefCV = &cv
	fc, parts := combineConfidence(flat)
	if !(fc > plain) {
		t.Fatalf("flat reference should shift weight onto reconciliation: %v vs %v", fc, plain)
	}
	if _, ok := parts["ref_cv"]; !ok {
		t.Fatalf("ref_cv should be surfaced in the breakdown: %v", parts)
	}
}

func TestSnoopFactorScreenedPool(t *testing.T) {
	// The same modest r must survive better when the multiple-comparison family is
	// the 4 physically-plausible meters instead of all 792 overheard ones.
	r := 0.3
	screened := snoopFactor(&r, 144, 4)
	everyone := snoopFactor(&r, 144, 792)
	if !(screened > everyone) {
		t.Fatalf("screened pool should relax the correction: k=4 %v vs k=792 %v", screened, everyone)
	}
}

func TestCombineConfidenceRewardsGoodMatch(t *testing.T) {
	good := 0.95
	weak := 0.2
	gc, _ := combineConfidence(confidenceSignals{
		R: &good, HasMultiplier: true, MeterKwh: 12, MonitoredKwh: 1,
		WindowPackets: 200, NBuckets: 60, NCandidates: 50,
	})
	wc, _ := combineConfidence(confidenceSignals{
		R: &weak, HasMultiplier: false,
		WindowPackets: 200, NBuckets: 60, NCandidates: 50,
	})
	if !(gc > 0.6) {
		t.Fatalf("a strong reconciling match should score high, got %v", gc)
	}
	if !(gc > wc) {
		t.Fatalf("strong match (%v) should beat weak (%v)", gc, wc)
	}
	// a candidate that reads LESS energy than the monitored subset it must contain
	// should be down-weighted by the reconciliation gate.
	impossible := 0.9
	ic, _ := combineConfidence(confidenceSignals{
		R: &impossible, HasMultiplier: true, MeterKwh: 0.1, MonitoredKwh: 5,
		WindowPackets: 200, NBuckets: 60, NCandidates: 50,
	})
	if !(ic < gc) {
		t.Fatalf("an energy-impossible match (%v) should rank below a consistent one (%v)", ic, gc)
	}
}
