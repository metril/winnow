package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"winnow/internal/config"
	"winnow/internal/db"
	"winnow/internal/ert"
	"winnow/internal/ha"
	"winnow/internal/model"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func badReq(w http.ResponseWriter, msg string) { http.Error(w, msg, http.StatusBadRequest) }

// emptyToNil normalizes an optional string pointer: nil or "" → nil.
func emptyToNil(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

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

// handleDeleteMeter removes a meter's annotation (untrack). With ?purge=true it
// also deletes the meter's stored readings and registry rows.
func (s *server) handleDeleteMeter(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		badReq(w, "bad id")
		return
	}
	purge := r.URL.Query().Get("purge") == "true"
	if err := s.d.DeleteMeter(r.Context(), id, purge); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = s.d.NotifyConfig(r.Context()) // worker drops it from its publish set
	writeJSON(w, map[string]any{"ok": true, "purged": purge})
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

// handleUtilityStatistics lists the HA long-term ENERGY statistics (e.g. the
// Opower/utility integration) the user can pick as their billed-energy source.
func (s *server) handleUtilityStatistics(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.d.LoadConfig(r.Context())
	if !cfg.HAConfigured() {
		writeJSON(w, []ha.StatID{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	stats, err := ha.ListEnergyStatisticIDs(ctx, cfg.HAURL, cfg.HAToken)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	writeJSON(w, stats)
}

// handleUtilitySeries returns the configured statistic's billed-energy series
// (kWh + cost) with the published meter's recorded energy for reconciliation.
func (s *server) handleUtilitySeries(w http.ResponseWriter, r *http.Request) {
	res, err := s.d.UtilitySeries(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, res)
}

// handleUtilityCompare returns one meter's bill-vs-meter comparison (per billing
// bucket + estimated-daily breakdown for monthly bills).
func (s *server) handleUtilityCompare(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		badReq(w, "bad id")
		return
	}
	res, err := s.d.UtilityCompare(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, res)
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

// --- devices & scan settings ------------------------------------------------

func (s *server) handleDevices(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.d.LoadConfig(r.Context())
	devs, err := s.d.ListDevices(r.Context(), 60*time.Second)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for i := range devs {
		ov := cfg.Capture.Devices[devs[i].Serial] // raw per-dongle overrides (empty = inherit)
		devs[i].Enabled = cfg.Capture.DeviceEnabled(devs[i].Serial)
		devs[i].Label = ov.Label
		devs[i].Freq, devs[i].Gain, devs[i].PPM = ov.Freq, ov.Gain, ov.PPM
		devs[i].MsgType, devs[i].FilterID = ov.MsgType, ov.FilterID
	}
	writeJSON(w, map[string]any{
		"devices": devs,
		// global defaults inherited by any dongle that doesn't override them
		"defaults": map[string]any{
			"freq": cfg.Capture.Freq, "gain": cfg.Capture.Gain, "ppm": cfg.Capture.PPM,
			"msgtype": cfg.Capture.MsgType, "filterid": cfg.Capture.FilterID,
		},
	})
}

// handlePutDevice updates one dongle's enable/gain/label in the capture_devices
// map and NOTIFYs capture to hot-apply it.
func (s *server) handlePutDevice(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")
	if serial == "" {
		badReq(w, "bad serial")
		return
	}
	var body config.DeviceConfig
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badReq(w, "bad json")
		return
	}
	cfg, _ := s.d.LoadConfig(r.Context())
	devices := cfg.Capture.Devices
	if devices == nil {
		devices = map[string]config.DeviceConfig{}
	}
	devices[serial] = body
	blob, _ := json.Marshal(devices)
	if err := s.d.SetSetting(r.Context(), config.KeyCaptureDevices, string(blob)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = s.d.NotifyConfig(r.Context())
	writeJSON(w, map[string]any{"ok": true})
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
	// Correlation bucket: explicit minutes via ?bucket=, else "auto" scaled to the
	// window so the number of points stays reasonable.
	bucketMin := db.PickBucketMin(int(hours * 60))
	if b := r.URL.Query().Get("bucket"); b != "" && b != "auto" {
		if v, err := strconv.Atoi(strings.TrimSuffix(b, "m")); err == nil && v > 0 {
			bucketMin = v
		}
	}
	// Commodity: the monitored reference is electrical power, so default to ranking
	// electric meters only; ?commodity=all includes gas/water.
	commodity := "electric"
	if c := r.URL.Query().Get("commodity"); c == "all" {
		commodity = "all"
	}
	// Daily physics screen first (window-independent): its survivors define the
	// data-snooping pool and its verdicts gate/boost per-meter confidence.
	var aux *db.IdentifyAux
	var survivors int
	if screen, serr := s.d.DailyReconciliation(r.Context(), cfg.MonitoredEntities, cfg.HATimeZone, nil); serr == nil {
		aux = db.AuxFromScreen(screen)
		survivors = screen.Survivors
	}
	ranking, floor, err := s.d.CorrelationVsReferenceAux(r.Context(), cfg.MonitoredEntities, start, end, bucketMin, commodity == "electric", aux)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{
		"start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339),
		"monitored_entities":   cfg.MonitoredEntities,
		"monitored_floor_w":    floor,
		"monitored_energy_kwh": s.d.MonitoredEnergy(r.Context(), cfg.MonitoredEntities, start, end),
		"monitored_cv":         s.d.MonitoredCV(r.Context(), cfg.MonitoredEntities, start, end),
		"bucket_min":           bucketMin,
		"commodity":            commodity,
		"physics_survivors":    survivors,
		"ranking":              ranking,
	})
}

// handleIdentifyDaily returns the daily-reconciliation physics screen: per-local-
// day energy of every plausible meter vs the monitored reference and the bill
// band. ?ids=1,2 adds those meters to the rows (with their failure reason) so the
// UI can chart any meter against the monitored/estimate lines.
func (s *server) handleIdentifyDaily(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.d.LoadConfig(r.Context())
	var ids []int64
	for _, p := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			if n, err := strconv.ParseInt(p, 10, 64); err == nil {
				ids = append(ids, n)
			}
		}
	}
	screen, err := s.d.DailyReconciliation(r.Context(), cfg.MonitoredEntities, cfg.HATimeZone, ids)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Bill-derived daily estimate (flat + shaped), aligned to the screen's days,
	// so the chart can draw the estimate lines next to monitored + meters.
	flat := map[string]float64{}
	shaped := map[string]*float64{}
	if cfg.UtilityConfigured() && len(screen.Days) > 0 {
		for _, e := range s.d.UtilityDailyEstimateRange(r.Context(), screen.Days[0], screen.Days[len(screen.Days)-1]) {
			flat[e.Day] = e.FlatKwh
			shaped[e.Day] = e.ShapedKwh
		}
	}
	flatArr := make([]*float64, len(screen.Days))
	shapedArr := make([]*float64, len(screen.Days))
	for i, day := range screen.Days {
		if v, ok := flat[day]; ok {
			vv := v
			flatArr[i] = &vv
		}
		shapedArr[i] = shaped[day]
	}
	writeJSON(w, map[string]any{
		"screen":         screen,
		"flat_estimate":  flatArr,
		"shaped_estimate": shapedArr,
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

// --- analytics --------------------------------------------------------------

func intParam(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func (s *server) handleProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		badReq(w, "bad id")
		return
	}
	days := intParam(r, "days", 14)
	var (
		out any
		err error
	)
	switch r.URL.Query().Get("type") {
	case "dow":
		out, err = s.d.DowProfile(r.Context(), id, days)
	case "daily":
		out, err = s.d.DailyRollup(r.Context(), id, days)
	case "heatmap":
		out, err = s.d.Heatmap(r.Context(), id, days)
	default: // "hod"
		out, err = s.d.HourlyProfile(r.Context(), id, days)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

func (s *server) handleBenchmark(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		badReq(w, "bad id")
		return
	}
	b, err := s.d.BenchmarkMeter(r.Context(), id, intParam(r, "days", 7))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, b)
}

func (s *server) handleCoverage(w http.ResponseWriter, r *http.Request) {
	cells, err := s.d.CoverageMatrix(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Annotate each distinct source with a friendly label and whether the dongle is
	// still present (in sdr_devices). The coverage matrix is all-time, so departed
	// dongles linger in meter_source; the UI uses `present` to hide them by default.
	cfg, _ := s.d.LoadConfig(r.Context())
	devs, _ := s.d.ListDevices(r.Context(), 60*time.Second)
	present := map[string]string{} // serial -> hardware name
	for _, d := range devs {
		present[d.Serial] = d.Name
	}
	seen := map[string]bool{}
	type covSource struct {
		Source  string `json:"source"`
		Label   string `json:"label"`
		Present bool   `json:"present"`
	}
	sources := []covSource{}
	for _, c := range cells {
		if seen[c.Source] {
			continue
		}
		seen[c.Source] = true
		name, ok := present[c.Source]
		label := cfg.Capture.Devices[c.Source].Label
		if label == "" {
			label = name
		}
		if label == "" {
			label = c.Source
		}
		sources = append(sources, covSource{Source: c.Source, Label: label, Present: ok})
	}
	writeJSON(w, map[string]any{"cells": cells, "sources": sources})
}

func (s *server) handleSourceTimeline(w http.ResponseWriter, r *http.Request) {
	since := timeParam(r, "since", time.Now().Add(-6*time.Hour))
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = "5m"
	}
	out, err := s.d.SourceTimeline(r.Context(), *since, bucket)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

// liveSources is the set of capture sources we currently expect to be producing
// data: a dongle that is both plugged in (present in sdr_devices) and enabled in
// config. Used to suppress phantom source_down alerts for departed/disabled dongles.
func (s *server) liveSources(r *http.Request) []string {
	cfg, _ := s.d.LoadConfig(r.Context())
	devs, err := s.d.ListDevices(r.Context(), 60*time.Second)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, d := range devs {
		if cfg.Capture.DeviceEnabled(d.Serial) {
			out = append(out, d.Serial)
		}
	}
	return out
}

func (s *server) handleAnomalies(w http.ResponseWriter, r *http.Request) {
	a, err := s.d.Anomalies(r.Context(), s.liveSources(r))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, a)
}

// handleOverview is the glanceable dashboard payload: each published meter with
// its live rate, today's consumption and estimated cost, plus anomalies.
func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.d.LoadConfig(r.Context())
	pubs, _ := s.d.MetersForPublish(r.Context())
	midnight := time.Now().UTC().Truncate(24 * time.Hour)
	type pubRow struct {
		EndpointID int64    `json:"endpoint_id"`
		Name       string   `json:"name"`
		Commodity  string   `json:"commodity"`
		Unit       string   `json:"unit"`
		Multiplier float64  `json:"multiplier"`
		Rate       *float64 `json:"rate"`       // current units/hour (×multiplier)
		TodayValue float64  `json:"today"`      // consumption since midnight (×multiplier)
		CostToday  float64  `json:"cost_today"` // electric only
	}
	rowsOut := []pubRow{}
	for _, m := range pubs {
		row := pubRow{EndpointID: m.EndpointID, Commodity: m.Commodity, Multiplier: m.PubMultiplier}
		if m.PubName != nil {
			row.Name = *m.PubName
		}
		if m.PubUnit != nil {
			row.Unit = *m.PubUnit
		}
		row.TodayValue = round(s.d.MovementSince(r.Context(), m.EndpointID, midnight)*m.PubMultiplier, 3)
		if v, ok := s.d.DerivedPower(r.Context(), m.EndpointID, m.PubMultiplier); ok {
			v = round(v, 3)
			row.Rate = &v
		}
		if m.Commodity == "electric" && cfg.CostPerKwh > 0 {
			row.CostToday = round(row.TodayValue*cfg.CostPerKwh, 2)
		}
		rowsOut = append(rowsOut, row)
	}
	anomalies, _ := s.d.Anomalies(r.Context(), s.liveSources(r))
	writeJSON(w, map[string]any{
		"currency":     cfg.Currency,
		"cost_per_kwh": cfg.CostPerKwh,
		"published":    rowsOut,
		"anomalies":    anomalies,
	})
}

// --- admin / maintenance ----------------------------------------------------

func (s *server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.d.DBStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, st)
}

func (s *server) handleMaintenance(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Op string `json:"op"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badReq(w, "bad json")
		return
	}
	var err error
	if body.Op == "prune_devices" {
		err = s.d.PruneDevices(r.Context(), s.liveSources(r))
	} else {
		err = s.d.RunMaintenance(r.Context(), body.Op)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = s.d.NotifyConfig(r.Context())
	writeJSON(w, map[string]any{"ok": true, "op": body.Op})
}

// handleAdminDelete performs guarded data deletion. Scoped deletes require
// confirm=="DELETE"; the catastrophic purge-all requires confirm=="PURGE-ALL".
func (s *server) handleAdminDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode    string `json:"mode"`
		Days    int    `json:"days"`
		Source  string `json:"source"`
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badReq(w, "bad json")
		return
	}
	need := "DELETE"
	if body.Mode == "all_readings" {
		need = "PURGE-ALL"
	}
	if body.Confirm != need {
		badReq(w, "confirmation required")
		return
	}
	var removed int64
	var err error
	switch body.Mode {
	case "age":
		removed, err = s.d.DeleteReadingsOlderThan(r.Context(), body.Days)
	case "source":
		removed, err = s.d.DeleteReadingsBySource(r.Context(), body.Source)
	case "all_tests":
		removed, err = s.d.PurgeTests(r.Context())
	case "all_readings":
		err = s.d.PurgeAllReadings(r.Context())
	default:
		badReq(w, "unknown mode")
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = s.d.NotifyConfig(r.Context())
	writeJSON(w, map[string]any{"ok": true, "mode": body.Mode, "removed": removed})
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
		Label         string   `json:"label"`
		StartTS       string   `json:"start_ts"`
		EndTS         string   `json:"end_ts"`
		KnownLoadW    *float64 `json:"known_load_w"`
		KnownEntityID *string  `json:"known_entity_id"`
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
	t, err := s.d.CreateTest(r.Context(), body.Label, start.UTC(), end, "manual", body.KnownLoadW, emptyToNil(body.KnownEntityID))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, t)
}

func (s *server) handleStartTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label         string   `json:"label"`
		KnownLoadW    *float64 `json:"known_load_w"`
		KnownEntityID *string  `json:"known_entity_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Label == "" {
		body.Label = "load test"
	}
	t, err := s.d.CreateTest(r.Context(), body.Label, time.Now().UTC(), nil, "manual", body.KnownLoadW, emptyToNil(body.KnownEntityID))
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
	// Freeze the data-snooping candidate pool for this window at close time so
	// re-analyses don't drift as more meters are overheard later. Best-effort.
	if cfg, cerr := s.d.LoadConfig(r.Context()); cerr == nil {
		if screen, serr := s.d.DailyReconciliation(r.Context(), cfg.MonitoredEntities, cfg.HATimeZone, nil); serr == nil && screen.Survivors > 0 {
			_ = s.d.SetTestSnoopK(r.Context(), id, screen.Survivors)
		}
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
	cfg, _ := s.d.LoadConfig(r.Context())
	res, err := s.d.CombinedRanking(r.Context(), cfg.MonitoredEntities)
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
	// When ground-truth sensors are configured, rank this window against them (so
	// the per-test view shows the composite confidence and the known-load anchor);
	// otherwise fall back to the rate-ratio correlation.
	cfg, _ := s.d.LoadConfig(r.Context())
	var ranking []model.CorrRow
	if len(cfg.MonitoredEntities) > 0 {
		bm := db.PickBucketMin(int(end.Sub(start.UTC()).Minutes()))
		ranking, _, err = s.d.CorrelationVsReference(r.Context(), cfg.MonitoredEntities, start.UTC(), end, bm, true)
		if err == nil {
			// known-load anchor for this window → a direct multiplier per candidate.
			expected := 0.0
			if t.KnownLoadW != nil {
				expected = *t.KnownLoadW * end.Sub(start.UTC()).Hours() / 1000.0
			} else if t.KnownEntityID != nil {
				expected = s.d.EntityEnergy(r.Context(), *t.KnownEntityID, start.UTC(), end)
			}
			if expected > 0 {
				for i := range ranking {
					if ranking[i].WindowDelta > 0 {
						am := expected / ranking[i].WindowDelta
						ranking[i].AnchorMultiplier = &am
					}
				}
			}
		}
	} else {
		ranking, err = s.d.Correlation(r.Context(), start.UTC(), end)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"test": t, "end_used": end.Format(time.RFC3339), "ranking": ranking})
}
