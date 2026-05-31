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

	haRestart chan struct{} // signal the HA loop to reconnect with new cfg
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

	w := &worker{d: d, pub: mqtt.NewPublisher(), publishSet: map[int64]model.Meter{}, haRestart: make(chan struct{}, 1)}
	defer w.pub.Close()
	w.reloadConfig(ctx)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); w.listenLoop(ctx) }()
	go func() { defer wg.Done(); w.haLoop(ctx) }()
	wg.Wait()
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
		threshold := cfg.ThresholdW
		client := ha.New(cfg.HAURL, cfg.HAToken)
		info := buildEntityInfo(ctx, client, entities)
		log.Printf("[worker] HA WS subscribing to %d monitored entit(ies); threshold %.0fW", len(entities), threshold)

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
			w.autoWindow(subCtx, agg, threshold)
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

// autoWindow opens/closes a source='auto' test window on threshold crossings.
func (w *worker) autoWindow(ctx context.Context, power, threshold float64) {
	open, isOpen := w.d.OpenWindow(ctx, "auto")
	if power >= threshold && !isOpen {
		_, _ = w.d.CreateTest(ctx, "auto load "+time.Now().UTC().Format("15:04"), time.Now().UTC(), nil, "auto")
		log.Printf("[worker] auto window opened (%.0fW ≥ %.0fW)", power, threshold)
	} else if power < threshold && isOpen {
		_, _ = w.d.StopTest(ctx, open.ID, time.Now().UTC())
		log.Printf("[worker] auto window closed (%.0fW < %.0fW)", power, threshold)
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
