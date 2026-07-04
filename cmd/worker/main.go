// Command worker is winnow's singleton event hub. It runs the HA WebSocket
// subscription (plug power → reference samples + auto test windows), maintains
// the MQTT publisher, and LISTENs for Postgres NOTIFYs to publish meter state
// to Home Assistant the instant a reading lands. No polling.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
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

	haRestart   chan struct{} // signal the HA loop to reconnect with new cfg
	utilRestart chan struct{} // signal the utility backfill loop to re-run with new cfg

	aw autoState // auto-window detector state (touched only by the single-threaded HA callback)
}

// autoState is the live state of the baseline-relative auto-window detector.
type autoState struct {
	inited       bool
	baseline     float64   // rolling estimate of idle (always-on) power, W
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

// onReading publishes a meter's state to HA if it's flagged publish.
func (w *worker) onReading(ctx context.Context, id int64) {
	w.mu.RLock()
	m, ok := w.publishSet[id]
	w.mu.RUnlock()
	if !ok || !w.pub.Connected() {
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
			if !waitOrRestart(ctx, 5*time.Second, w.haRestart) {
				return
			}
			continue
		}

		entities := cfg.MonitoredEntities
		openDelta := cfg.ThresholdW
		autoOn := cfg.AutoWindow
		client := ha.New(cfg.HAURL, cfg.HAToken)
		info := buildEntityInfo(ctx, client, entities)
		log.Printf("[worker] HA WS subscribing to %d monitored entit(ies); auto-window=%v (Δ%.0fW)", len(entities), autoOn, openDelta)

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

		err := ha.Stream(subCtx, cfg.HAURL, cfg.HAToken, entities, func(entity string, s ha.Sample) {
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
			_ = w.d.InsertReferenceSample(subCtx, entity, s.TS, powerW)
			lastPower[entity] = powerW
			agg := 0.0
			for _, v := range lastPower {
				agg += v
			}
			w.d.NotifyReference(subCtx, agg)
			w.autoWindow(subCtx, agg, openDelta, autoOn)
		})
		cancel()
		if err != nil && ctx.Err() == nil {
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
func buildEntityInfo(ctx context.Context, client *ha.Client, entities []string) map[string]einfo {
	out := map[string]einfo{}
	sensors, err := client.MonitorableSensors(ctx)
	if err != nil {
		log.Printf("[worker] entity info: %v", err)
		return out
	}
	byID := map[string]ha.Entity{}
	for _, s := range sensors {
		byID[s.EntityID] = s
	}
	for _, e := range entities {
		s, ok := byID[e]
		if !ok {
			log.Printf("[worker] monitored entity %s not found in HA states", e)
			continue
		}
		out[e] = einfo{kind: s.Kind, factor: unitFactor(s.Kind, s.Unit)}
	}
	return out
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

// auto-window detector tuning. The window opens on a sustained rise of openDelta
// watts above the rolling baseline and closes on a fall back toward it (hysteresis),
// with a hard duration cap and a cooldown so it can never run forever.
const (
	autoMaxDuration = 15 * time.Minute
	autoCooldown    = 5 * time.Minute
	autoBaselineA   = 0.02 // EWMA weight for the idle-power baseline
)

// autoWindow runs the baseline-relative auto-window state machine. It's invoked on
// every monitored-power sample with the summed power, the open delta (watts above
// baseline, from threshold_w), and whether the opt-in feature is enabled. Called
// only from the single-threaded HA callback, so w.aw needs no locking.
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
		// feature disabled (the default): self-heal any window left open
		if w.aw.open {
			_, _ = w.d.StopTest(ctx, w.aw.openID, now)
			w.aw.open, w.aw.lastClosedAt = false, now
			log.Printf("[worker] auto window closed (feature off)")
		}
		return
	}

	if openDelta <= 0 {
		openDelta = 50
	}
	closeDelta := openDelta * 0.5

	if !w.aw.open {
		// track the idle baseline only while no window is open, so the spike itself
		// doesn't drag the baseline up and mask the close.
		w.aw.baseline = w.aw.baseline*(1-autoBaselineA) + power*autoBaselineA
		if power >= w.aw.baseline+openDelta && now.Sub(w.aw.lastClosedAt) >= autoCooldown {
			if t, err := w.d.CreateTest(ctx, "auto load "+now.Format("15:04"), now, nil, "auto", nil, nil); err == nil {
				w.aw.open, w.aw.openID, w.aw.openedAt = true, t.ID, now
				log.Printf("[worker] auto window opened (%.0fW ≥ baseline %.0f + %.0f)", power, w.aw.baseline, openDelta)
			}
		}
		return
	}

	// open: close on fall-back (hysteresis) or the hard duration cap
	if power < w.aw.baseline+closeDelta || now.Sub(w.aw.openedAt) >= autoMaxDuration {
		_, _ = w.d.StopTest(ctx, w.aw.openID, now)
		w.aw.open, w.aw.lastClosedAt = false, now
		log.Printf("[worker] auto window closed (%.0fW, after %s)", power, now.Sub(w.aw.openedAt).Round(time.Second))
	}
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
