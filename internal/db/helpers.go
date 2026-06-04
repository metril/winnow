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
