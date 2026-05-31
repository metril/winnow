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
