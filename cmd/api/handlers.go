package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"winnow/internal/config"
	"winnow/internal/db"
	"winnow/internal/ert"
	"winnow/internal/ha"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func badReq(w http.ResponseWriter, msg string) { http.Error(w, msg, http.StatusBadRequest) }

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

// timeParam parses an RFC3339 query param; falls back to def.
func timeParam(r *http.Request, key string, def time.Time) *time.Time {
	v := r.URL.Query().Get(key)
	if v == "" {
		return &def
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		tt := t.UTC()
		return &tt
	}
	return &def
}

// --- health & stream --------------------------------------------------------

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	h, err := s.d.Health(r.Context(), 60*time.Second)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, h)
}

func (s *server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.broker.add()
	defer s.broker.remove(ch)
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// --- meters -----------------------------------------------------------------

func (s *server) handleMeters(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	o := db.LeaderboardOpts{
		Since:          timeParam(r, "since", time.Now().Add(-24*time.Hour)),
		MsgType:        q.Get("msg_type"),
		ElectricOnly:   q.Get("electric_only") == "true",
		IncludeIgnored: q.Get("include_ignored") == "true",
		TrackedOnly:    q.Get("tracked_only") == "true",
		PublishedOnly:  q.Get("published_only") == "true",
	}
	if u := q.Get("until"); u != "" {
		o.Until = timeParam(r, "until", time.Now())
	}
	meters, err := s.d.Leaderboard(r.Context(), o)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, meters)
}

func (s *server) handleMeterDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		badReq(w, "bad id")
		return
	}
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = "1h"
	}
	since := timeParam(r, "since", time.Now().Add(-24*time.Hour))
	var until *time.Time
	if r.URL.Query().Get("until") != "" {
		until = timeParam(r, "until", time.Now())
	}
	series, err := s.d.MeterSeries(r.Context(), id, since, until, bucket)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	meter, _ := s.d.GetMeter(r.Context(), id)
	writeJSON(w, map[string]any{"endpoint_id": id, "bucket": bucket,
		"points": series.Points, "deltas": series.Deltas, "annotation": meter})
}

func (s *server) handleMeterPatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		badReq(w, "bad id")
		return
	}
	var body struct {
		Label         *string  `json:"label"`
		Notes         *string  `json:"notes"`
		IsCandidate   *bool    `json:"is_candidate"`
		IsMine        *bool    `json:"is_mine"`
		Ignored       *bool    `json:"ignored"`
		Publish       *bool    `json:"publish"`
		PubName       *string  `json:"pub_name"`
		PubMultiplier *float64 `json:"pub_multiplier"`
		PubUnit       *string  `json:"pub_unit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badReq(w, "bad json")
		return
	}
	m, err := s.d.UpdateMeter(r.Context(), id, db.MeterUpdate(body))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = s.d.NotifyConfig(r.Context()) // worker refreshes its publish set
	writeJSON(w, m)
}

func (s *server) handleFilterCommand(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		badReq(w, "bad id")
		return
	}
	mt, found := s.d.LatestMsgType(r.Context(), id)
	if !found {
		http.Error(w, "meter not seen", 404)
		return
	}
	cmd := fmt.Sprintf("rtlamr -filterid=%d -msgtype=%s -format=json", id, ert.MsgTypeToken(mt))
	writeJSON(w, map[string]any{"endpoint_id": id, "msg_type": mt, "command": cmd})
}

func (s *server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		badReq(w, "bad id")
		return
	}
	since := timeParam(r, "since", time.Now().Add(-7*24*time.Hour))
	rows, err := s.d.MeterReadings(r.Context(), id, since, nil)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="winnow_%d.csv"`, id))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"ts", "msg_type", "endpoint_id", "endpoint_type", "consumption", "source"})
	for _, rd := range rows {
		et, cons := "", ""
		if rd.EndpointType != nil {
			et = strconv.Itoa(*rd.EndpointType)
		}
		if rd.Consumption != nil {
			cons = strconv.FormatFloat(*rd.Consumption, 'f', -1, 64)
		}
		_ = cw.Write([]string{rd.TS.UTC().Format(time.RFC3339Nano), rd.MsgType,
			strconv.FormatInt(rd.EndpointID, 10), et, cons, rd.Source})
	}
	cw.Flush()
}

func (s *server) handleSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var ids []int64
	for _, p := range strings.Split(q.Get("ids"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			if n, err := strconv.ParseInt(p, 10, 64); err == nil {
				ids = append(ids, n)
			}
		}
	}
	bucket := q.Get("bucket")
	if bucket == "" {
		bucket = "5m"
	}
	mode := q.Get("mode")
	if mode == "" {
		mode = "delta"
	}
	since := timeParam(r, "since", time.Now().Add(-6*time.Hour))
	out, err := s.d.MultiSeries(r.Context(), ids, since, nil, bucket, mode)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

// --- settings & integrations ------------------------------------------------

func (s *server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	m, err := s.d.GetSettings(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := map[string]any{}
	for k, v := range m {
		if config.SecretKeys[k] {
			out[k] = ""
			out[k+"_set"] = v != ""
		} else {
			out[k] = v
		}
	}
	writeJSON(w, out)
}

func (s *server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badReq(w, "bad json")
		return
	}
	for k, v := range body {
		if config.SecretKeys[k] && (v == "" || strings.Contains(v, "*")) {
			continue // don't overwrite a secret with a blank/masked value
		}
		if err := s.d.SetSetting(r.Context(), k, v); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	_ = s.d.NotifyConfig(r.Context())
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) handleIntegrationsTest(w http.ResponseWriter, r *http.Request) {
	overrides := map[string]string{}
	_ = json.NewDecoder(r.Body).Decode(&overrides)
	stored, _ := s.d.GetSettings(r.Context())
	for k, v := range overrides {
		if v != "" && !strings.Contains(v, "*") {
			stored[k] = v
		}
	}
	cfg := config.FromMap(stored)

	haOK, haErr := true, ""
	if cfg.HAConfigured() {
		if err := ha.New(cfg.HAURL, cfg.HAToken).Test(r.Context()); err != nil {
			haOK, haErr = false, err.Error()
		}
	} else {
		haOK, haErr = false, "not configured"
	}

	mqOK, mqErr := mqttReachable(cfg.MQTTHost, cfg.MQTTPort)
	writeJSON(w, map[string]any{
		"ha":   map[string]any{"ok": haOK, "error": haErr},
		"mqtt": map[string]any{"ok": mqOK, "error": mqErr},
	})
}

func (s *server) handleIntegrationsStatus(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.d.LoadConfig(r.Context())
	haOK := false
	if cfg.HAConfigured() {
		haOK = ha.New(cfg.HAURL, cfg.HAToken).Test(r.Context()) == nil
	}
	mqOK, _ := mqttReachable(cfg.MQTTHost, cfg.MQTTPort)
	pub, _ := s.d.MetersForPublish(r.Context())
	floor := s.d.MonitoredFloor(r.Context(), cfg.MonitoredEntities, time.Now().Add(-24*time.Hour), time.Now())
	writeJSON(w, map[string]any{
		"ha_ok": haOK, "mqtt_ok": mqOK,
		"monitored_entities": cfg.MonitoredEntities,
		"monitored_floor_w":  floor,
		"published":          pub,
	})
}

func mqttReachable(host string, port int) (bool, string) {
	if host == "" {
		return false, "not configured"
	}
	c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 4*time.Second)
	if err != nil {
		return false, err.Error()
	}
	c.Close()
	return true, ""
}

func (s *server) handlePowerEntities(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.d.LoadConfig(r.Context())
	if !cfg.HAConfigured() {
		writeJSON(w, []ha.Entity{})
		return
	}
	ents, err := ha.New(cfg.HAURL, cfg.HAToken).MonitorableSensors(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	writeJSON(w, ents)
}

// handleCreateHelper asks HA to create a Group "sum" sensor over the selected
// entities, then sets it as the monitored set. Falls back to an error the UI
// turns into manual-setup instructions.
func (s *server) handleCreateHelper(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string   `json:"name"`
		Entities []string `json:"entities"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Entities) == 0 {
		badReq(w, "need name + entities")
		return
	}
	if body.Name == "" {
		body.Name = "winnow monitored power"
	}
	cfg, _ := s.d.LoadConfig(r.Context())
	if !cfg.HAConfigured() {
		http.Error(w, "Home Assistant not configured", 400)
		return
	}
	client := ha.New(cfg.HAURL, cfg.HAToken)
	// device_class = energy only if every selected entity is energy, else power.
	deviceClass := "power"
	if kinds, err := client.EntityKinds(r.Context(), body.Entities); err == nil {
		allEnergy := true
		for _, e := range body.Entities {
			if kinds[e] != "energy" {
				allEnergy = false
				break
			}
		}
		if allEnergy {
			deviceClass = "energy"
		}
	}
	entityID, err := client.CreateGroupSum(r.Context(), body.Name, body.Entities, deviceClass)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	blob, _ := json.Marshal([]string{entityID})
	_ = s.d.SetSetting(r.Context(), config.KeyMonitoredEntities, string(blob))
	_ = s.d.NotifyConfig(r.Context())
	writeJSON(w, map[string]any{"ok": true, "entity_id": entityID})
}

// --- identify ---------------------------------------------------------------

func (s *server) handleIdentify(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.d.LoadConfig(r.Context())
	hours := 2.0
	if h := r.URL.Query().Get("hours"); h != "" {
		if v, err := strconv.ParseFloat(h, 64); err == nil && v > 0 {
			hours = v
		}
	}
	end := time.Now().UTC()
	start := end.Add(-time.Duration(hours * float64(time.Hour)))
	ranking, floor, err := s.d.CorrelationVsReference(r.Context(), cfg.MonitoredEntities, start, end)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{
		"start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339),
		"monitored_entities": cfg.MonitoredEntities,
		"monitored_floor_w":  floor,
		"ranking":            ranking,
	})
}

func (s *server) handleIdentifyAuto(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.d.LoadConfig(r.Context())
	res, err := s.d.IdentifyAuto(r.Context(), cfg.MonitoredEntities)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, res)
}

func (s *server) handleReferenceSeries(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.d.LoadConfig(r.Context())
	start := timeParam(r, "start", time.Now().Add(-2*time.Hour))
	end := timeParam(r, "end", time.Now())
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = "5m"
	}
	out, err := s.d.AggregateSeries(r.Context(), cfg.MonitoredEntities, *start, *end, bucket)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

// --- test windows -----------------------------------------------------------

func (s *server) handleListTests(w http.ResponseWriter, r *http.Request) {
	t, err := s.d.ListTests(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, t)
}

func (s *server) handleCreateTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label   string `json:"label"`
		StartTS string `json:"start_ts"`
		EndTS   string `json:"end_ts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badReq(w, "bad json")
		return
	}
	start, err := time.Parse(time.RFC3339, body.StartTS)
	if err != nil {
		badReq(w, "bad start_ts")
		return
	}
	var end *time.Time
	if body.EndTS != "" {
		if e, err := time.Parse(time.RFC3339, body.EndTS); err == nil {
			eu := e.UTC()
			end = &eu
		}
	}
	if body.Label == "" {
		body.Label = "load test"
	}
	t, err := s.d.CreateTest(r.Context(), body.Label, start.UTC(), end, "manual")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, t)
}

func (s *server) handleStartTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label string `json:"label"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Label == "" {
		body.Label = "load test"
	}
	t, err := s.d.CreateTest(r.Context(), body.Label, time.Now().UTC(), nil, "manual")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, t)
}

func (s *server) handleStopTest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		badReq(w, "bad id")
		return
	}
	t, err := s.d.StopTest(r.Context(), id, time.Now().UTC())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, t)
}

func (s *server) handleDeleteTest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		badReq(w, "bad id")
		return
	}
	if err := s.d.DeleteTest(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) handleCombined(w http.ResponseWriter, r *http.Request) {
	res, err := s.d.CombinedRanking(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, res)
}

func (s *server) handleTestCorrelation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		badReq(w, "bad id")
		return
	}
	t, err := s.d.GetTest(r.Context(), id)
	if err != nil {
		http.Error(w, "test not found", 404)
		return
	}
	start, _ := time.Parse(time.RFC3339Nano, t.StartTS)
	end := time.Now().UTC()
	if t.EndTS != nil {
		if e, err := time.Parse(time.RFC3339Nano, *t.EndTS); err == nil {
			end = e
		}
	}
	ranking, err := s.d.Correlation(r.Context(), start.UTC(), end)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"test": t, "end_used": end.Format(time.RFC3339), "ranking": ranking})
}
