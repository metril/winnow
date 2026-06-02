// Package ert handles rtlamr message decoding concerns shared by the capture
// ingester and the data layer: field extraction (with cross-message-type
// fallbacks) and commodity classification.
package ert

import (
	"encoding/json"
	"time"

	"winnow/internal/model"
)

// ElectricEndpointTypes is the common (not perfectly standardized) set of ERT
// endpoint-type codes for electric meters. Used for the electric-only filter.
var electricTypes = map[int]bool{4: true, 5: true, 7: true, 8: true, 12: true, 13: true}

// Commodity returns a best-effort commodity label for an ERT endpoint type.
func Commodity(t *int) string {
	if t == nil {
		return "unknown"
	}
	switch {
	case electricTypes[*t]:
		return "electric"
	case *t == 2 || *t == 9 || (*t >= 156 && *t <= 160):
		return "gas"
	case *t == 11 || *t == 171 || *t == 172:
		return "water"
	default:
		return "other"
	}
}

// MsgTypeToken maps a stored message-type string to an rtlamr -msgtype token.
func MsgTypeToken(msgType string) string {
	switch msgType {
	case "SCM":
		return "scm"
	case "SCM+":
		return "scm+"
	case "IDM":
		return "idm"
	case "NetIDM":
		return "netidm"
	default:
		return "scm"
	}
}

// rtlamr JSON shape (only the fields we read).
type rtlamrLine struct {
	Time    string                 `json:"Time"`
	Type    string                 `json:"Type"`
	Message map[string]json.Number `json:"Message"`
}

// ExtractReading parses one rtlamr JSON line into a Reading. ok=false on a
// line we can't use (bad JSON, or no id/consumption).
func ExtractReading(line []byte, source string) (model.Reading, bool) {
	var l rtlamrLine
	if err := json.Unmarshal(line, &l); err != nil {
		return model.Reading{}, false
	}
	id, idOK := pickInt(l.Message, "EndpointID", "ID", "ERTSerialNumber")
	cons, consOK := pickFloat(l.Message, "Consumption", "LastConsumptionCount", "LastConsumption")
	if !idOK || !consOK {
		return model.Reading{}, false
	}
	// Reject consumption <= 0 as a bad decode. The fields we read are cumulative
	// dial/counter totals (Consumption / LastConsumptionCount) that are never 0 for
	// an in-service meter; a 0 is CRC-passing garbage that would otherwise crash the
	// cumulative chart to zero and poison meter_index.min_consumption.
	if cons <= 0 {
		return model.Reading{}, false
	}
	etype, etOK := pickInt(l.Message, "EndpointType", "Type")

	ts := parseTime(l.Time)
	r := model.Reading{
		TS:          ts,
		MsgType:     l.Type,
		EndpointID:  id,
		Consumption: &cons,
		Source:      source,
	}
	if etOK {
		e := int(etype)
		r.EndpointType = &e
	}
	return r, true
}

func pickInt(m map[string]json.Number, keys ...string) (int64, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if n, err := v.Int64(); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

func pickFloat(m map[string]json.Number, keys ...string) (float64, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if f, err := v.Float64(); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

// parseTime normalizes rtlamr's Time to UTC; falls back to now on parse failure.
func parseTime(s string) time.Time {
	if s != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC()
			}
		}
	}
	return time.Now().UTC()
}
