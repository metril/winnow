package ha

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClimateEntities(t *testing.T) {
	states := []map[string]any{
		{
			"entity_id": "climate.upstairs",
			"state":     "cool",
			"attributes": map[string]any{
				"friendly_name": "Upstairs",
				"hvac_action":   "cooling",
			},
		},
		{
			"entity_id": "climate.downstairs",
			"state":     "off",
			"attributes": map[string]any{
				"friendly_name": "Downstairs",
			},
		},
		{
			"entity_id":  "sensor.power",
			"state":      "120",
			"attributes": map[string]any{"friendly_name": "Power"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/states" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(states)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	got, err := c.ClimateEntities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 climate entities, got %d: %+v", len(got), got)
	}
	byID := map[string]ClimateEntity{}
	for _, e := range got {
		byID[e.EntityID] = e
	}
	up, ok := byID["climate.upstairs"]
	if !ok {
		t.Fatalf("missing climate.upstairs in %+v", got)
	}
	if !up.HasAction || up.HVACAction != "cooling" || up.Name != "Upstairs" || up.State != "cool" {
		t.Fatalf("climate.upstairs = %+v", up)
	}
	down, ok := byID["climate.downstairs"]
	if !ok {
		t.Fatalf("missing climate.downstairs in %+v", got)
	}
	if down.HasAction || down.HVACAction != "" {
		t.Fatalf("climate.downstairs = %+v, want no hvac_action", down)
	}
	if _, ok := byID["sensor.power"]; ok {
		t.Fatalf("sensor.power leaked into climate entities: %+v", got)
	}
}

func TestClimateEntitiesEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"entity_id": "sensor.power", "state": "1", "attributes": map[string]any{}}})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	got, err := c.ClimateEntities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("want empty non-nil slice, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("want 0 climate entities, got %d", len(got))
	}
}

func TestAttrHistory(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	history := [][]map[string]any{
		{
			{
				"entity_id":    "climate.t",
				"state":        "cool",
				"last_updated": start.Add(1 * time.Minute).Format(time.RFC3339),
				"attributes":   map[string]any{"hvac_action": "cooling"},
			},
			{
				"entity_id":    "climate.t",
				"state":        "cool",
				"last_updated": start.Add(2 * time.Minute).Format(time.RFC3339),
				"attributes":   map[string]any{"hvac_action": "idle"},
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/history/period/") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		q := r.URL.RawQuery
		if !strings.Contains(q, "significant_changes_only=0") {
			t.Fatalf("query missing significant_changes_only=0: %s", q)
		}
		if strings.Contains(q, "minimal_response") {
			t.Fatalf("query must not include minimal_response (it strips attributes): %s", q)
		}
		_ = json.NewEncoder(w).Encode(history)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	got, err := c.AttrHistory(context.Background(), "climate.t", start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(got), got)
	}
	if got[0].Attr("hvac_action") != "cooling" || got[1].Attr("hvac_action") != "idle" {
		t.Fatalf("events = %+v, want cooling then idle in order", got)
	}
	if got[0].State != "cool" || got[1].State != "cool" {
		t.Fatalf("state mismatch: %+v", got)
	}
	if !got[0].TS.Equal(start.Add(1 * time.Minute)) {
		t.Fatalf("got[0].TS = %v, want %v (the served last_updated)", got[0].TS, start.Add(1*time.Minute))
	}
	if !got[1].TS.Equal(start.Add(2 * time.Minute)) {
		t.Fatalf("got[1].TS = %v, want %v (the served last_updated)", got[1].TS, start.Add(2*time.Minute))
	}
}

func TestAttrHistoryEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([][]map[string]any{})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	got, err := c.AttrHistory(context.Background(), "climate.t", time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("want empty non-nil slice, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("want 0 events, got %d", len(got))
	}
}
