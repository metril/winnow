package db

import (
	"math"
	"strings"
)

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func joinComma(s []string) string { return strings.Join(s, ", ") }

// PickBucketMin chooses a correlation bucket size (in minutes) for a window of
// windowMin minutes, targeting ~150 points and snapping to a sane ladder so the
// number of points stays reasonable as the analysis window grows.
func PickBucketMin(windowMin int) int {
	const targetPoints = 150
	ladder := []int{1, 2, 5, 10, 15, 30, 60, 120, 240}
	want := windowMin / targetPoints
	for _, b := range ladder {
		if b >= want {
			return b
		}
	}
	return ladder[len(ladder)-1]
}

// counterModulus returns the wrap-around boundary of a meter's cumulative
// consumption counter by message type: SCM packs Consumption in 24 bits (wraps at
// 2^24), while SCM+/IDM/NetIDM use a 32-bit field (2^32). A negative step smaller
// than the modulus is a rollover (add the modulus); a larger unexplained drop is a
// genuine reset. Unknown types default to the wider 32-bit boundary (conservative —
// it never turns a real reset into a spurious rollover for the common case).
func counterModulus(msgType string) float64 {
	switch msgType {
	case "SCM":
		return 16777216.0 // 2^24
	default:
		return 4294967296.0 // 2^32 (SCM+, IDM, NetIDM, unknown)
	}
}

// rolloverDeltaSQL builds a SQL expression for a rollover-/reset-aware cross-bucket
// delta. raw is the signed counter step (cmax - lag(cmax)); modCol is a column/expr
// holding that meter's counterModulus. A non-negative step passes through; a
// negative step that wraps forward by less than half the range is treated as a
// rollover (raw + modulus); anything else is a genuine reset → NULL.
func rolloverDeltaSQL(raw, modCol string) string {
	return "(CASE " +
		"WHEN " + raw + " >= 0 THEN " + raw + " " +
		"WHEN " + raw + " + " + modCol + " >= 0 AND " + raw + " + " + modCol + " < " + modCol + " * 0.5 THEN " + raw + " + " + modCol + " " +
		"ELSE NULL END)"
}

// demingSlope returns the total-least-squares (orthogonal, equal error-variance)
// slope of y on x from the regression sums sxx, syy, sxy. Unlike OLS, it does not
// attenuate when x (the monitored reference) is itself noisy. Falls back to the OLS
// slope sxy/sxx when sxy is ~0 (no relationship) to avoid a divide-by-zero.
func demingSlope(sxx, syy, sxy float64) float64 {
	if math.Abs(sxy) < 1e-12 {
		if sxx == 0 {
			return 0
		}
		return sxy / sxx
	}
	return (syy - sxx + math.Sqrt((syy-sxx)*(syy-sxx)+4*sxy*sxy)) / (2 * sxy)
}

// bucketInterval maps a UI bucket token to a Postgres interval literal.
func bucketInterval(bucket string) string {
	switch bucket {
	case "1d":
		return "1 day"
	case "5m":
		return "5 minutes"
	default:
		return "1 hour"
	}
}
