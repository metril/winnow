// Command worker is winnow's singleton event hub. It runs the HA WebSocket
// subscription (plug power → reference samples + auto test windows), maintains
// the MQTT publisher, and LISTENs for Postgres NOTIFYs to publish meter state
// to Home Assistant the instant a reading lands. No polling.
package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"winnow/internal/config"
	"winnow/internal/db"
	"winnow/internal/ha"
	"winnow/internal/model"
	"winnow/internal/mqtt"
)

type worker struct {
	d   *db.DB
	pub *mqtt.Publisher

	mu         sync.RWMutex
	cfg        config.Config
	publishSet map[int64]model.Meter // meters to publish, by id
	pubNoted   map[int64]pubNote     // last publish outcome written per meter (throttle)

	haRestart   chan struct{} // signal the HA loop to reconnect with new cfg
	utilRestart chan struct{} // signal the utility backfill loop to re-run with new cfg

	aw autoState // auto-window detector state (touched only by the single-threaded HA callback)

	// einfoCache holds the last successfully-resolved entity info (guarded by
	// mu; written only by haLoop). Units and kinds don't change, so during an
	// HA restart — when /api/states errors or returns half-loaded entities —
	// the cache keeps the subscription meaningful instead of silently empty.
	einfoCache map[string]einfo

	// worker_status is one settings row shared by the MQTT and HA-stream
	// facts; stMu guards the composed fields so both writers stay merged.
	stMu          sync.Mutex
	mqttDetail    string
	haStreamState string
	lastRefInsert atomic.Int64 // unix seconds of the last successful reference insert
}

// putWorkerStatus writes the composed worker_status row. Callers mutate their
// fields under stMu first; this is transition-driven, never polled.
func (w *worker) putWorkerStatus(ctx context.Context) {
	w.stMu.Lock()
	ws := db.WorkerStatus{MQTTConnected: w.pub.Connected(), Detail: w.mqttDetail, HAStream: w.haStreamState}
	w.stMu.Unlock()
	if t := w.lastRefInsert.Load(); t > 0 {
		ws.HALastEventTS = time.Unix(t, 0).UTC().Format(time.RFC3339)
	}
	_ = w.d.SetWorkerStatus(ctx, ws)
}

// setHAStream records the HA stream state when it CHANGES — the hot path calls
// this per event, so equal states never touch the DB.
func (w *worker) setHAStream(ctx context.Context, state string) {
	w.stMu.Lock()
	same := w.haStreamState == state
	w.haStreamState = state
	w.stMu.Unlock()
	if !same {
		w.putWorkerStatus(ctx)
	}
}

// autoState is the live state of the baseline-relative auto-window detector.
type autoState struct {
	inited       bool
	baseline     float64   // rolling estimate of idle (always-on) power, W
	dev          float64   // rolling |power − baseline| (noise floor for the adaptive threshold)
	open         bool      // an auto window is currently open
	openID       int64     // its test_windows id
	openedAt     time.Time // when it opened (for the max-duration cap)
	lastClosedAt time.Time // when the last one closed (for the cooldown)
}

func main() {
	log.SetFlags(log.LstdFlags)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		cancel()
	}()

	d := mustDB(ctx)
	defer d.Close()
	if err := d.InitSchema(ctx); err != nil {
		log.Printf("[worker] schema init: %v", err)
	}

	w := &worker{d: d, pub: mqtt.NewPublisher(), publishSet: map[int64]model.Meter{},
		haRestart: make(chan struct{}, 1), utilRestart: make(chan struct{}, 1)}
	defer w.pub.Close()
	w.reloadConfig(ctx)

	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); w.listenLoop(ctx) }()
	go func() { defer wg.Done(); w.haLoop(ctx) }()
	go func() { defer wg.Done(); w.utilityLoop(ctx) }()
	go func() { defer wg.Done(); w.caggBackfillLoop(ctx) }()
	wg.Wait()
}

// caggBackfillLoop materializes readings_1h over stored history exactly once,
// in 7-day ranges, resumable via a settings watermark, then exits. It runs here
// rather than in InitSchema because a full refresh over weeks of readings would
// hold the schema advisory lock and stall capture ingest at deploy time. The
// aggregate's refresh policy owns everything newer than 48h, and real-time
// aggregation answers for any not-yet-materialized range in the meantime — this
// loop just makes old history cheap to read.
func (w *worker) caggBackfillLoop(ctx context.Context) {
	const key = "readings_1h_refreshed_to"
	const step = 7 * 24 * time.Hour
	for ctx.Err() == nil {
		settings, err := w.d.GetSettings(ctx)
		if err != nil {
			if !sleepCtx(ctx, 30*time.Second) {
				return
			}
			continue
		}
		var from time.Time
		if v := settings[key]; v != "" {
			if t, perr := time.Parse(time.RFC3339, v); perr == nil {
				from = t
			}
		}
		if from.IsZero() {
			oldest, ok := w.d.OldestReadingTS(ctx)
			if !ok {
				// fresh install: nothing to backfill, the policy owns the future
				_ = w.d.SetSetting(ctx, key, time.Now().UTC().Format(time.RFC3339))
				return
			}
			from = oldest.UTC().Truncate(time.Hour)
		}
		target := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
		if !from.Before(target) {
			return // caught up to the policy's start_offset — done for good
		}
		to := from.Add(step)
		if to.After(target) {
			to = target
		}
		if err := w.d.RefreshReadings1h(ctx, from, to); err != nil {
			log.Printf("[worker] readings_1h backfill %s..%s: %v", from.Format(time.RFC3339), to.Format(time.RFC3339), err)
			if !sleepCtx(ctx, 30*time.Second) {
				return
			}
			continue
		}
		if err := w.d.SetSetting(ctx, key, to.Format(time.RFC3339)); err != nil {
			log.Printf("[worker] readings_1h watermark: %v", err)
		}
		log.Printf("[worker] readings_1h materialized through %s", to.Format(time.RFC3339))
		if !sleepCtx(ctx, 2*time.Second) { // let ingest breathe between ranges
			return
		}
	}
}

// sleepCtx waits d or until ctx is done; false = shutting down.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func mustDB(ctx context.Context) *db.DB {
	for attempt := 1; attempt <= 30; attempt++ {
		d, err := db.New(ctx, config.DatabaseURL())
		if err == nil {
			return d
		}
		log.Printf("[worker] waiting for db (%d): %v", attempt, err)
		time.Sleep(2 * time.Second)
	}
	log.Fatal("[worker] database unreachable")
	return nil
}

// reloadConfig re-reads settings, reconnects MQTT if configured, refreshes the
// publish set, and nudges the HA loop to re-subscribe.
func (w *worker) reloadConfig(ctx context.Context) {
	cfg, err := w.d.LoadConfig(ctx)
	if err != nil {
		log.Printf("[worker] load config: %v", err)
		return
	}
	w.mu.Lock()
	prevHost, prevUser := w.cfg.MQTTHost, w.cfg.MQTTUser
	w.cfg = cfg
	w.mu.Unlock()

	if cfg.MQTTConfigured() && (!w.pub.Connected() || prevHost != cfg.MQTTHost || prevUser != cfg.MQTTUser) {
		if err := w.pub.Connect(cfg); err != nil {
			log.Printf("[worker] mqtt connect: %v", err)
		} else {
			log.Printf("[worker] mqtt connected to %s:%d", cfg.MQTTHost, cfg.MQTTPort)
		}
	}
	// report the broker session truthfully so the API/UI never have to infer it
	// from a TCP dial
	detail := ""
	if !cfg.MQTTConfigured() {
		detail = "not configured"
	}
	w.stMu.Lock()
	w.mqttDetail = detail
	w.stMu.Unlock()
	w.putWorkerStatus(ctx)
	w.refreshPublishSet(ctx)
	select {
	case w.haRestart <- struct{}{}:
	default:
	}
	select {
	case w.utilRestart <- struct{}{}:
	default:
	}
}

func (w *worker) refreshPublishSet(ctx context.Context) {
	meters, err := w.d.MetersForPublish(ctx)
	if err != nil {
		log.Printf("[worker] publish set: %v", err)
		return
	}
	next := map[int64]model.Meter{}
	for _, m := range meters {
		next[m.EndpointID] = m
	}
	w.mu.Lock()
	prev := w.publishSet
	w.publishSet = next
	w.mu.Unlock()
	// Remove discovery for meters no longer published.
	for id, m := range prev {
		if _, ok := next[id]; !ok {
			w.pub.Remove(m)
		}
	}
}

// listenLoop dispatches Postgres NOTIFYs: reading → publish; config → reload.
func (w *worker) listenLoop(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := w.d.Pool().Acquire(ctx)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		_, _ = conn.Exec(ctx, "LISTEN winnow")
		_, _ = conn.Exec(ctx, "LISTEN winnow_config")
		log.Printf("[worker] listening for notifications")
		for ctx.Err() == nil {
			n, err := conn.Conn().WaitForNotification(ctx)
			if err != nil {
				break
			}
			switch n.Channel {
			case "winnow_config":
				w.reloadConfig(ctx)
			case "winnow":
				if id, err := strconv.ParseInt(n.Payload, 10, 64); err == nil {
					w.onReading(ctx, id)
				}
			}
		}
		conn.Release()
		time.Sleep(time.Second)
	}
}

// onReading publishes a meter's state to HA if it's flagged publish, and
// records the real outcome in publish_status — a disconnected broker used to
// no-op silently while the UI toasted "Publishing to HA".
func (w *worker) onReading(ctx context.Context, id int64) {
	w.mu.RLock()
	m, ok := w.publishSet[id]
	w.mu.RUnlock()
	if !ok {
		return
	}
	if !w.pub.Connected() {
		w.notePublish(ctx, id, false, "mqtt broker not connected")
		return
	}
	var energy *float64
	if _, c, ok := w.d.LatestReading(ctx, id); ok {
		e := c * m.PubMultiplier
		energy = &e
	}
	var power *float64
	if p, ok := w.d.DerivedPower(ctx, id, m.PubMultiplier); ok {
		power = &p
	}
	signal := w.d.SignalPerHour(ctx, id)
	w.pub.PublishState(m, energy, power, signal)
	w.notePublish(ctx, id, true, "")
}

// notePublish persists a publish outcome, throttled per meter: repeat successes
// write at most every 30s (chatty meters would otherwise hammer the table), but
// any success↔error transition writes immediately.
func (w *worker) notePublish(ctx context.Context, id int64, ok bool, errMsg string) {
	w.mu.Lock()
	if w.pubNoted == nil {
		w.pubNoted = map[int64]pubNote{}
	}
	prev, seen := w.pubNoted[id]
	transition := !seen || prev.ok != ok || (!ok && prev.err != errMsg)
	if !transition && ok && time.Since(prev.at) < 30*time.Second {
		w.mu.Unlock()
		return
	}
	w.pubNoted[id] = pubNote{ok: ok, err: errMsg, at: time.Now()}
	w.mu.Unlock()
	if ok {
		_ = w.d.RecordPublishOK(ctx, id)
	} else {
		_ = w.d.RecordPublishError(ctx, id, errMsg)
	}
}

type pubNote struct {
	ok  bool
	err string
	at  time.Time
}

// einfo is a monitored entity's normalization metadata.
type einfo struct {
	kind   string  // "power" | "energy"
	factor float64 // power: unit→W; energy: unit→kWh
}

// haLoop subscribes to ALL monitored entities, normalizes each to watts (energy
// sensors are differentiated), and feeds the live aggregate (sum) to auto-window
// detection + SSE. Reconnects with backoff and on config change.
func (w *worker) haLoop(ctx context.Context) {
	for ctx.Err() == nil {
		w.mu.RLock()
		cfg := w.cfg
		w.mu.RUnlock()

		if !cfg.HAConfigured() || !cfg.ReferenceConfigured() {
			w.setHAStream(ctx, "not configured")
			if !waitOrRestart(ctx, 5*time.Second, w.haRestart) {
				return
			}
			continue
		}

		entities := cfg.MonitoredEntities
		openDelta := cfg.ThresholdW
		autoOn := cfg.AutoWindow
		hvacOn := cfg.HVACConfigured()
		subEntities := entities
		if hvacOn {
			subEntities = append(append([]string{}, entities...), cfg.HVACEntityID)
		}
		client := ha.New(cfg.HAURL, cfg.HAToken)
		fresh, ferr := buildEntityInfo(ctx, client, entities)
		w.mu.RLock()
		cache := w.einfoCache
		w.mu.RUnlock()
		info, ok := resolveEntityInfo(fresh, cache, entities)
		if !ok {
			// Subscribing with nothing resolved would "succeed" and then
			// silently discard every event (the July 2026 outage) — treat it
			// as a failed connect and retry instead.
			reason := fmt.Sprintf("0 of %d monitored entities found in HA states", len(entities))
			if ferr != nil {
				reason = strings.TrimSpace(ferr.Error())
			}
			w.setHAStream(ctx, "entity resolution failed: "+reason)
			log.Printf("[worker] entity resolution failed (%s) — retrying, not subscribing blind", reason)
			if !waitOrRestart(ctx, 15*time.Second, w.haRestart) {
				return
			}
			continue
		}
		if ferr != nil {
			log.Printf("[worker] entity info: %v (continuing on cached info)", ferr)
		}
		for _, e := range entities {
			if _, found := info[e]; !found {
				log.Printf("[worker] monitored entity %s not in HA states and never seen before — skipped this subscription", e)
			}
		}
		w.mu.Lock()
		w.einfoCache = info
		w.mu.Unlock()
		log.Printf("[worker] HA WS subscribing to %d monitored entit(ies), %d resolved; auto-window=%v (Δ%.0fW)", len(entities), len(info), autoOn, openDelta)
		if hvacOn {
			log.Printf("[worker] HVAC estimate on: entity=%s heat=%.1fkW cool=%.1fkW", cfg.HVACEntityID, cfg.HVACHeatingKW, cfg.HVACCoolingKW)
		}

		// per-subscription normalization state (callback is single-threaded)
		lastPower := map[string]float64{}
		type estate struct {
			have    bool
			lastKwh float64
			lastTS  time.Time
		}
		es := map[string]*estate{}

		subCtx, cancel := context.WithCancel(ctx)
		go func() {
			select {
			case <-subCtx.Done():
			case <-w.haRestart:
				cancel()
			}
		}()

		// Keepalive: HA power sensors report on CHANGE, so a steady load can be
		// legitimately silent — indistinguishable from a dead feed. While the
		// subscription is healthy, re-insert each entity's last value every 5
		// minutes, so downstream "no samples for >15 min" means exactly one
		// thing: the feed is dead. This is what makes the bounded gap-fill safe.
		var kaMu sync.Mutex
		type kaSample struct {
			ts time.Time
			w  float64
		}
		kaLast := map[string]kaSample{}
		// hvac tracks the last known hvac_action for the live aggregate + its
		// own keepalive; guarded by kaMu like kaLast (the ticker touches both).
		// hvacKA is defined in hvac.go alongside the pure hvacTransition helper.
		var hvac hvacKA

		if hvacOn {
			// Seed a sample on (re)connect: without it, an HVAC entity that's
			// been idle/off since before this subscription started contributes
			// nothing to the aggregate until its next hvac_action change, which
			// can be hours away. Best-effort — a failure here must never block
			// the monitored subscription below.
			sctx, scancel := context.WithTimeout(subCtx, 15*time.Second)
			climates, cerr := client.ClimateEntities(sctx)
			scancel()
			found := false
			for _, ce := range climates {
				if ce.EntityID != cfg.HVACEntityID {
					continue
				}
				found = true
				if ce.HasAction && ce.HVACAction != "" {
					now := time.Now()
					if w.d.InsertHVACSample(subCtx, cfg.HVACEntityID, now, ce.HVACAction) == nil {
						kaMu.Lock()
						hvac = hvacKA{set: true, action: ce.HVACAction, ts: now}
						kaMu.Unlock()
					}
				} else {
					log.Printf("[worker] HVAC entity %s has no active hvac_action yet — seed skipped", cfg.HVACEntityID)
				}
				break
			}
			if cerr != nil {
				log.Printf("[worker] HVAC seed: climate entities: %v", cerr)
			} else if !found {
				log.Printf("[worker] HVAC entity %s not found in HA states — seed skipped", cfg.HVACEntityID)
			}
		}

		go func() {
			t := time.NewTicker(5 * time.Minute)
			defer t.Stop()
			for {
				select {
				case <-subCtx.Done():
					return
				case <-t.C:
					now := time.Now()
					kaMu.Lock()
					for e, s := range kaLast {
						if now.Sub(s.ts) >= 5*time.Minute {
							if w.d.InsertReferenceSample(subCtx, e, now, s.w) == nil {
								kaLast[e] = kaSample{ts: now, w: s.w}
								w.lastRefInsert.Store(now.Unix())
							}
						}
					}
					if hvac.set && now.Sub(hvac.ts) >= 5*time.Minute {
						if w.d.InsertHVACSample(subCtx, cfg.HVACEntityID, now, hvac.action) == nil {
							hvac.ts = now
						}
					}
					kaMu.Unlock()
				}
			}
		}()

		// publish computes the live aggregate (monitored entities + the HVAC
		// estimate) and pushes it to the SSE overlay + auto-window detector.
		// Called from both onSample and onState so they always agree.
		publish := func() {
			kaMu.Lock()
			agg := 0.0
			for _, v := range lastPower {
				agg += v
			}
			if hvac.set {
				agg += hvacWatts(hvac.action, cfg.HVACHeatingKW, cfg.HVACCoolingKW)
			}
			kaMu.Unlock()
			w.d.NotifyReference(subCtx, agg)
			w.autoWindow(subCtx, agg, openDelta, autoOn)
		}

		onSample := func(entity string, s ha.Sample) {
			in, ok := info[entity]
			if !ok {
				return
			}
			var powerW float64
			switch in.kind {
			case "power":
				powerW = s.Power * in.factor
			case "energy":
				kwh := s.Power * in.factor
				st := es[entity]
				if st == nil {
					st = &estate{}
					es[entity] = st
				}
				if !st.have {
					st.have, st.lastKwh, st.lastTS = true, kwh, s.TS
					return // need two points to differentiate
				}
				dt := s.TS.Sub(st.lastTS).Hours()
				prevKwh := st.lastKwh
				st.lastKwh, st.lastTS = kwh, s.TS
				if dt <= 0 {
					return
				}
				powerW = (kwh - prevKwh) * 1000 / dt
				if powerW < 0 {
					powerW = 0 // utility_meter cycle reset
				}
			default:
				return
			}
			if w.d.InsertReferenceSample(subCtx, entity, s.TS, powerW) == nil {
				now := time.Now().Unix()
				if prev := w.lastRefInsert.Swap(now); prev > 0 && now-prev > int64((15*time.Minute).Seconds()) {
					// the feed just came back after a real outage: heal the hole
					// from HA statistics now, not at the next 12h utility tick
					select {
					case w.utilRestart <- struct{}{}:
					default:
					}
				}
				w.setHAStream(subCtx, "ok")
			}
			kaMu.Lock()
			kaLast[entity] = kaSample{ts: s.TS, w: powerW}
			kaMu.Unlock()
			lastPower[entity] = powerW
			publish()
		}

		// onState is StreamStates' non-numeric callback: only the configured
		// HVAC entity's hvac_action is of interest here (monitored entities'
		// power/energy samples are handled by onSample above).
		onState := func(entity string, ev ha.StateEvent) {
			if !hvacOn || entity != cfg.HVACEntityID {
				return
			}
			action := ev.Attr("hvac_action")
			if action == "" {
				// The thermostat reported no hvac_action (e.g. it just went
				// unavailable): stop asserting it's still running and
				// republish the aggregate without it — otherwise the 5-min
				// keepalive would keep inserting the last-known action for
				// the whole outage, fabricating kW-scale energy.
				kaMu.Lock()
				hvac = hvacTransition(hvac, "", ev.TS)
				kaMu.Unlock()
				publish()
				return
			}
			if w.d.InsertHVACSample(subCtx, entity, ev.TS, action) == nil {
				kaMu.Lock()
				hvac = hvacTransition(hvac, action, ev.TS)
				kaMu.Unlock()
			}
			publish()
		}

		err := ha.StreamStates(subCtx, cfg.HAURL, cfg.HAToken, subEntities, onSample, onState)
		cancel()
		if err != nil && ctx.Err() == nil {
			w.setHAStream(ctx, "reconnecting: "+trim(strings.TrimSpace(err.Error()), 120))
			log.Printf("[worker] HA WS ended: %v (reconnect in 5s)", err)
			if !waitOrRestart(ctx, 5*time.Second, w.haRestart) {
				return
			}
		}
	}
}

// utilityLoop keeps the billed-energy table (utility_energy) fresh from HA
// long-term statistics. This is an EXPLICIT scheduled backfill, NOT live polling:
// utility data is only released ~twice daily with a ~48h lag and has no live
// state to subscribe to, so a 12h cadence (plus startup + on config change)
// matches the source and respects winnow's no-live-polling rule.
func (w *worker) utilityLoop(ctx context.Context) {
	for ctx.Err() == nil {
		w.mu.RLock()
		cfg := w.cfg
		w.mu.RUnlock()
		if cfg.HAConfigured() && cfg.ReferenceConfigured() {
			w.backfillReferenceGaps(ctx, cfg)
		}
		if cfg.HAConfigured() && cfg.HVACConfigured() {
			w.backfillHVACHistory(ctx, cfg)
		}
		if cfg.HAConfigured() && cfg.UtilityConfigured() {
			if err := w.backfillUtility(ctx, cfg); err != nil {
				log.Printf("[worker] utility backfill: %v", err)
			}
		}
		if !waitOrRestart(ctx, 12*time.Hour, w.utilRestart) {
			return
		}
	}
}

// backfillReferenceGaps reconstructs reference_samples across feed outages from
// HA long-term statistics (hourly means — HA keeps them indefinitely), so an
// outage like June 2026's 23-day silence costs identification evidence only
// until the next worker run, not forever. Backfilled rows are tagged
// src='lts_backfill' and replaced idempotently; live rows are never touched.
func (w *worker) backfillReferenceGaps(ctx context.Context, cfg config.Config) {
	// LTS hourly buckets are only complete through the previous full hour, and a
	// live-feed outage should still read as STALE on the dashboard — cap the
	// backfill 2h back so it can't mask a current outage with near-now samples.
	capEnd := time.Now().Add(-2 * time.Hour)
	gaps := w.d.ReferenceGaps(ctx, cfg.MonitoredEntities, 60*24*time.Hour, 30*time.Minute, capEnd)
	if len(gaps) == 0 {
		return
	}
	fctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	client := ha.New(cfg.HAURL, cfg.HAToken)
	fresh, ferr := buildEntityInfo(fctx, client, cfg.MonitoredEntities)
	if ferr != nil {
		log.Printf("[worker] entity info: %v", ferr)
	}
	w.mu.RLock()
	cache := w.einfoCache
	w.mu.RUnlock()
	info, ok := resolveEntityInfo(fresh, cache, cfg.MonitoredEntities)
	if !ok {
		log.Printf("[worker] reference backfill skipped: no monitored entities resolved (retries within 12h)")
		return
	}
	for _, g := range gaps {
		lo, hi := g[0], g[1]
		if hi.After(capEnd) {
			hi = capEnd
		}
		if !lo.Before(hi) {
			continue
		}
		// fetch from one hour before the gap so the first partial hour is covered
		means, err := ha.StatisticsMeanDuringPeriod(fctx, cfg.HAURL, cfg.HAToken,
			cfg.MonitoredEntities, lo.Add(-time.Hour), hi)
		if err != nil {
			log.Printf("[worker] reference backfill %s..%s: %v", lo.Format(time.RFC3339), hi.Format(time.RFC3339), err)
			return
		}
		filled := 0
		for entity, pts := range means {
			in, ok := info[entity]
			if !ok {
				continue
			}
			var ts []time.Time
			var pw []float64
			for _, p := range pts {
				var powerW float64
				switch {
				case in.kind == "power" && p.Mean != nil:
					powerW = *p.Mean * in.factor
				case in.kind == "energy" && p.Change != nil:
					// hourly consumed energy (stats unit → kWh) → average W
					powerW = *p.Change * in.factor * 1000
				default:
					continue
				}
				if powerW < 0 {
					powerW = 0
				}
				// 5-minute density inside the hour: dense enough that the bounded
				// gap-fill integrates the hourly mean exactly
				for m := 0; m < 60; m += 5 {
					t := p.Start.Add(time.Duration(m) * time.Minute)
					if t.Before(lo) || t.After(hi) {
						continue
					}
					ts = append(ts, t)
					pw = append(pw, powerW)
				}
			}
			if err := w.d.ReplaceBackfillSamples(ctx, entity, lo, hi, ts, pw); err != nil {
				log.Printf("[worker] reference backfill insert %s: %v", entity, err)
				continue
			}
			filled += len(ts)
		}
		if filled > 0 {
			log.Printf("[worker] reference backfill: %s..%s filled with %d statistic samples",
				lo.Format(time.RFC3339), hi.Format(time.RFC3339), filled)
		}
	}
}

// backfillHVACHistory reconstructs hvac_samples across gaps from HA state
// history (the recorder's default ~10-day retention), mirroring
// backfillReferenceGaps: idempotent, tagged history_backfill, live rows never
// touched. A freshly configured entity has no history before its first live/
// seed sample, so the leading gap (before the first sample) matters here in a
// way it doesn't for the always-already-populated reference feed. All range
// planning (the leading gap, clamping to capEnd, dropping emptied ranges) is
// the pure planHVACBackfill (hvac.go); this function only does I/O.
func (w *worker) backfillHVACHistory(ctx context.Context, cfg config.Config) {
	entity := cfg.HVACEntityID
	now := time.Now()
	capEnd := now.Add(-2 * time.Hour)
	lookback := 10 * 24 * time.Hour
	minGap := 30 * time.Minute

	first, _ := w.d.HVACSpan(ctx, entity)
	var gaps [][2]time.Time
	if first != nil {
		gaps = w.d.HVACGaps(ctx, entity, lookback, minGap, capEnd)
	}
	ranges := planHVACBackfill(first, gaps, now, lookback, minGap, capEnd)
	if len(ranges) == 0 {
		return
	}
	fctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	client := ha.New(cfg.HAURL, cfg.HAToken)
	for _, g := range ranges {
		lo, hi := g[0], g[1]
		events, err := client.AttrHistory(fctx, entity, lo.Add(-time.Hour), hi)
		if err != nil {
			log.Printf("[worker] hvac backfill %s..%s: %v", lo.Format(time.RFC3339), hi.Format(time.RFC3339), err)
			return
		}
		ts, actions := expandHVACHistory(events, lo, hi, 5*time.Minute)
		if err := w.d.ReplaceHVACBackfill(ctx, entity, lo, hi, ts, actions); err != nil {
			log.Printf("[worker] hvac backfill insert %s..%s: %v", lo.Format(time.RFC3339), hi.Format(time.RFC3339), err)
			continue
		}
		if len(ts) > 0 {
			log.Printf("[worker] hvac backfill: %s..%s filled with %d samples",
				lo.Format(time.RFC3339), hi.Format(time.RFC3339), len(ts))
		}
	}
}

// backfillUtility fetches the configured statistic's full available history at the
// configured (or auto-probed) period, normalizes monotonic sums to per-bucket kWh,
// and idempotently upserts. The window starts far enough back that HA returns
// everything the utility exposes (Opower keeps ~3 yr); real depth is bounded by the
// utility's own retention, giving the user the full series to browse and plenty of
// billing buckets for cross-bucket multiplier stability.
func (w *worker) backfillUtility(ctx context.Context, cfg config.Config) error {
	fctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	end := time.Now().UTC()
	start := end.AddDate(-10, 0, 0) // max available — HA returns only what the utility retains
	statID := cfg.UtilityStatisticID

	// Cache HA's timezone so daily utility breakdowns align to the user's local
	// calendar day rather than UTC (cheap; HA buckets statistics in its own tz).
	if tz, terr := ha.New(cfg.HAURL, cfg.HAToken).TimeZone(fctx); terr == nil && tz != "" {
		_ = w.d.SetSetting(ctx, config.KeyHATimeZone, tz)
	}

	var period string
	var points []ha.StatPoint
	var err error
	if cfg.UtilityPeriod == "auto" || cfg.UtilityPeriod == "" {
		period, points, err = ha.ResolvePeriod(fctx, cfg.HAURL, cfg.HAToken, statID, start, end)
	} else {
		period = cfg.UtilityPeriod
		points, err = ha.StatisticsDuringPeriod(fctx, cfg.HAURL, cfg.HAToken, statID, start, end, period)
	}
	if err != nil {
		return err
	}
	samples := ha.BucketDeltas(points)
	if len(samples) == 0 {
		log.Printf("[worker] utility: no statistics returned for %s", statID)
		return nil
	}
	ts := make([]time.Time, len(samples))
	kwh := make([]float64, len(samples))
	for i, s := range samples {
		ts[i], kwh[i] = s.TS, s.Kwh
	}
	if err := w.d.UpsertUtilityEnergy(ctx, statID, period, ts, kwh); err != nil {
		return err
	}
	// drop any rows under a now-stale period/statistic so the evidence query is
	// unambiguous (e.g. after switching auto→month or picking a different stat).
	_ = w.d.KeepOnlyUtility(ctx, statID, period)
	log.Printf("[worker] utility backfill: %s period=%s buckets=%d", statID, period, len(samples))
	return nil
}

// buildEntityInfo resolves each monitored entity's kind + unit factor from HA.
// It returns only what resolved; callers merge with their last-good cache via
// resolveEntityInfo and decide whether the result is usable.
func buildEntityInfo(ctx context.Context, client *ha.Client, entities []string) (map[string]einfo, error) {
	out := map[string]einfo{}
	sensors, err := client.MonitorableSensors(ctx)
	if err != nil {
		return out, err
	}
	byID := map[string]ha.Entity{}
	for _, s := range sensors {
		byID[s.EntityID] = s
	}
	for _, e := range entities {
		if s, ok := byID[e]; ok {
			out[e] = einfo{kind: s.Kind, factor: unitFactor(s.Kind, s.Unit)}
		}
	}
	return out, nil
}

// resolveEntityInfo merges a fresh resolution with the last-good cache: fresh
// wins, the cache fills misses (an HA mid-restart returns errors or
// half-loaded entities; units and kinds don't change between boots). ok=false
// means ZERO of the requested entities resolved — subscribing then would
// "succeed" and silently discard every event (the July 2026 feed outage), so
// the caller must retry instead.
func resolveEntityInfo(fresh, cache map[string]einfo, entities []string) (map[string]einfo, bool) {
	out := map[string]einfo{}
	for _, e := range entities {
		if in, ok := fresh[e]; ok {
			out[e] = in
		} else if in, ok := cache[e]; ok {
			out[e] = in
		}
	}
	return out, len(out) > 0
}

// unitFactor converts a sensor's native unit to W (power) or kWh (energy).
func unitFactor(kind, unit string) float64 {
	switch strings.ToUpper(unit) {
	case "KW":
		return 1000
	case "WH":
		return 0.001
	case "MWH":
		return 1000
	default: // W, kWh, or unknown
		return 1
	}
}

// auto-window detector tuning. The window opens on a sustained rise above the
// rolling baseline and closes on a fall back toward it (hysteresis), with a hard
// duration cap and a cooldown so it can never run forever. The open threshold is
// noise-adaptive: on a home whose monitored load carries hundreds of watts of
// server jitter, a fixed threshold either never fires or fires constantly.
const (
	autoMaxDuration = 15 * time.Minute
	autoMinDuration = 3 * time.Minute // shorter = a blip, not evidence: discard
	autoCooldown    = 5 * time.Minute
	autoBaselineA   = 0.02 // EWMA weight for the idle-power baseline
	autoDevA        = 0.05 // EWMA weight for the noise (|power−baseline|) tracker
)

// autoAction is what the detector wants done after one sample.
type autoAction int

const (
	autoNone autoAction = iota
	autoOpen
	autoClose
	autoDiscard // close AND delete: too short to be evidence
)

// autoDecide advances the detector state for one summed-power sample and returns
// the action. Pure (mutates only aw, never the DB), so the whole open/close
// policy is unit-testable:
//   - open when power ≥ baseline + max(openDelta, 3×noise) after the cooldown;
//   - close when power < baseline + max(openDelta/2, 1.5×noise) or at the cap;
//   - a close within autoMinDuration is a discard.
func autoDecide(aw *autoState, power, openDelta float64, now time.Time) autoAction {
	if openDelta <= 0 {
		openDelta = 400
	}
	if !aw.open {
		// track baseline + noise only while closed, so the spike itself can't
		// drag the baseline up and mask the close
		aw.dev = aw.dev*(1-autoDevA) + math.Abs(power-aw.baseline)*autoDevA
		aw.baseline = aw.baseline*(1-autoBaselineA) + power*autoBaselineA
		if power >= aw.baseline+math.Max(openDelta, 3*aw.dev) && now.Sub(aw.lastClosedAt) >= autoCooldown {
			return autoOpen
		}
		return autoNone
	}
	if power < aw.baseline+math.Max(openDelta*0.5, 1.5*aw.dev) || now.Sub(aw.openedAt) >= autoMaxDuration {
		if now.Sub(aw.openedAt) < autoMinDuration {
			return autoDiscard
		}
		return autoClose
	}
	return autoNone
}

// autoWindow runs the baseline-relative auto-window state machine. It's invoked on
// every monitored-power sample with the summed power, the open delta (watts above
// baseline, from threshold_w), and whether the feature is enabled (default on).
// Called only from the single-threaded HA callback, so w.aw needs no locking.
func (w *worker) autoWindow(ctx context.Context, power, openDelta float64, on bool) {
	now := time.Now().UTC()
	if !w.aw.inited {
		// adopt a pre-existing open window so it's governed (and capped/closed), not orphaned
		if o, ok := w.d.OpenWindow(ctx, "auto"); ok {
			w.aw.open, w.aw.openID = true, o.ID
			if t, err := time.Parse(time.RFC3339Nano, o.StartTS); err == nil {
				w.aw.openedAt = t
			} else {
				w.aw.openedAt = now
			}
		}
		w.aw.baseline = power
		w.aw.inited = true
	}

	if !on {
		// feature disabled: self-heal any window left open
		if w.aw.open {
			_, _ = w.d.StopTest(ctx, w.aw.openID, now)
			w.aw.open, w.aw.lastClosedAt = false, now
			w.d.NotifyTests(ctx)
			log.Printf("[worker] auto window closed (feature off)")
		}
		return
	}

	switch autoDecide(&w.aw, power, openDelta, now) {
	case autoOpen:
		// the observed step IS the known load — it gives the window the direct
		// calibration anchor manual tests get from a typed-in wattage
		step := math.Round(power - w.aw.baseline)
		if t, err := w.d.CreateTest(ctx, "auto load "+now.Format("15:04"), now, nil, "auto", &step, nil); err == nil {
			w.aw.open, w.aw.openID, w.aw.openedAt = true, t.ID, now
			w.d.NotifyTests(ctx)
			log.Printf("[worker] auto window opened (%.0fW ≥ baseline %.0f, step %.0fW)", power, w.aw.baseline, step)
		}
	case autoClose:
		_, _ = w.d.StopTest(ctx, w.aw.openID, now)
		w.mu.RLock()
		cfg := w.cfg
		w.mu.RUnlock()
		w.d.FreezeTestSnoopK(ctx, w.aw.openID, cfg.MonitoredEntities, cfg.HATimeZone)
		w.aw.open, w.aw.lastClosedAt = false, now
		w.d.NotifyTests(ctx)
		log.Printf("[worker] auto window closed (%.0fW, after %s)", power, now.Sub(w.aw.openedAt).Round(time.Second))
	case autoDiscard:
		_ = w.d.DeleteTest(ctx, w.aw.openID)
		w.aw.open, w.aw.lastClosedAt = false, now
		w.d.NotifyTests(ctx)
		log.Printf("[worker] auto window discarded (blip, %s < %s)", now.Sub(w.aw.openedAt).Round(time.Second), autoMinDuration)
	}
}

// trim bounds a string for status rows (error text can embed whole URLs).
func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func waitOrRestart(ctx context.Context, d time.Duration, restart <-chan struct{}) bool {
	select {
	case <-ctx.Done():
		return false
	case <-restart:
		return true
	case <-time.After(d):
		return true
	}
}
