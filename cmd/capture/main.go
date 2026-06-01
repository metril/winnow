// Command capture supervises one rtl_tcp + rtlamr pair per RTL-SDR dongle,
// parses the decoded JSON, and stores readings (INSERT + NOTIFY) in TimescaleDB.
// Each dongle runs in its own goroutine and is restarted independently. --mock
// (or CAPTURE_MOCK=1) emits synthetic data for hardware-free runs.
package main

import (
	"bufio"
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"winnow/internal/config"
	"winnow/internal/db"
	"winnow/internal/ert"
)

func main() {
	mock := flag.Bool("mock", os.Getenv("CAPTURE_MOCK") == "1", "use synthetic data, no SDR")
	flag.Parse()
	log.SetFlags(log.LstdFlags)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	database := mustDB(ctx)
	defer database.Close()
	if err := database.InitSchema(ctx); err != nil {
		log.Printf("[capture] schema init: %v (continuing)", err)
	}

	if *mock {
		var wg sync.WaitGroup
		for _, src := range []string{"mock-0", "mock-1"} {
			wg.Add(1)
			go func(s string) { defer wg.Done(); superviseMock(ctx, database, s) }(src)
		}
		wg.Wait()
		return
	}
	// Live mode: a manager owns one supervisor per enabled dongle and reconciles
	// the running set whenever scan settings or the device selection change
	// (Postgres LISTEN), or a dongle is hot-plugged (periodic re-enumerate).
	(&manager{d: database, running: map[string]*runningDev{}}).run(ctx)
}

// scanParams is the effective tuning for one dongle. It's comparable so a change
// is detected with ==.
type scanParams struct {
	devIndex                        int
	freq, gain, ppm, msgtype, filt  string
}

type runningDev struct {
	cancel     context.CancelFunc
	params     scanParams
	done       chan struct{}
	up         *atomic.Bool // true while its rtl_tcp+rtlamr pipeline is ingesting
	downStreak int          // consecutive ticks observed not-up (health check)
}

type manager struct {
	d          *db.DB
	running    map[string]*runningDev
	inventory  []sdrDevice // enumerated while devices are free; refreshed on rescan
	lastRescan time.Time
}

// openLock serializes rtl_tcp device opens. librtlsdr enumerates the whole USB
// bus on open, so several receivers opening at once race and fail ("connection
// refused" as rtl_tcp dies before binding) — especially with a marginal dongle
// on a shared hub. Holding this during each open+bind window makes the good
// dongles come up cleanly regardless of a bad neighbour.
var openLock sync.Mutex

// run reconciles on startup, on config NOTIFYs, and on a periodic tick. The tick
// also runs a health check: if a dongle's pipeline stays down (e.g. it dropped
// off the bus, or the index map went stale after another dongle vanished), it
// triggers a coordinated rescan — stop everything, re-enumerate while the
// devices are free, and restart against the corrected inventory.
func (m *manager) run(ctx context.Context) {
	notify := m.listenConfig(ctx)
	m.reconcile(ctx, false)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-ticker.C:
			m.reconcile(ctx, true)
		case <-notify:
			log.Printf("[capture] config changed; reconciling devices")
			m.reconcile(ctx, false)
		}
	}
}

// rescanNeeded reports whether any running dongle has been down for two
// consecutive ticks (~60s) — a stuck pipeline that re-enumeration may fix. A
// healthy-but-quiet dongle stays "up" (blocked reading), so it never trips this.
func (m *manager) rescanNeeded() bool {
	if len(m.running) == 0 {
		return false
	}
	bad := false
	for _, rd := range m.running {
		if rd.up.Load() {
			rd.downStreak = 0
			continue
		}
		rd.downStreak++
		if rd.downStreak >= 2 {
			bad = true
		}
	}
	return bad
}

// enumerateOnce probes for dongles (retrying until some appear) and records the
// inventory. Done before any receiver starts, so serials read reliably.
func (m *manager) enumerateOnce(ctx context.Context) []sdrDevice {
	for ctx.Err() == nil {
		devs := enumerateRTL(ctx)
		if len(devs) > 0 {
			log.Printf("[capture] detected %d SDR(s): %s", len(devs), describe(devs))
			serials := make([]string, 0, len(devs))
			for _, dev := range devs {
				_ = m.d.UpsertDevice(ctx, dev.source, dev.index, dev.name, dev.tuner)
				serials = append(serials, dev.source)
			}
			// drop inventory rows for dongles that are no longer present
			_ = m.d.PruneDevices(ctx, serials)
			return devs
		}
		log.Printf("[capture] no SDRs detected; retrying in 5s")
		if !sleepCtx(ctx, 5*time.Second) {
			return nil
		}
	}
	return nil
}

// reconcile starts/stops/restarts supervisors so the running set matches the
// enabled+configured desired set derived from the cached inventory + DB config.
// When mayRescan is set (periodic tick), an unhealthy dongle triggers a
// coordinated re-enumeration first.
func (m *manager) reconcile(ctx context.Context, mayRescan bool) {
	if mayRescan && m.rescanNeeded() && time.Since(m.lastRescan) > 5*time.Minute {
		log.Printf("[capture] a dongle is unhealthy; rescanning the bus")
		m.stopAll()
		m.inventory = nil
		m.lastRescan = time.Now()
	}
	if len(m.inventory) == 0 {
		m.inventory = m.enumerateOnce(ctx)
	}
	cfg, err := m.d.LoadConfig(ctx)
	if err != nil {
		log.Printf("[capture] load config: %v", err)
		return
	}
	desired := map[string]scanParams{}
	for _, dev := range m.inventory {
		if !cfg.Capture.DeviceEnabled(dev.source) {
			continue
		}
		desired[dev.source] = scanParams{
			devIndex: dev.index,
			freq:     cfg.Capture.DeviceFreq(dev.source),
			gain:     cfg.Capture.DeviceGain(dev.source),
			ppm:      cfg.Capture.DevicePPM(dev.source),
			msgtype:  cfg.Capture.DeviceMsgType(dev.source),
			filt:     cfg.Capture.DeviceFilterID(dev.source),
		}
	}
	// stop devices that vanished, got disabled, or whose params changed
	for src, rd := range m.running {
		if want, ok := desired[src]; !ok || want != rd.params {
			log.Printf("[capture] stopping source=%s", src)
			rd.cancel()
			<-rd.done
			delete(m.running, src)
		}
	}
	// start devices that should be running but aren't
	for src, want := range desired {
		if _, ok := m.running[src]; ok {
			continue
		}
		dctx, dcancel := context.WithCancel(ctx)
		done := make(chan struct{})
		up := &atomic.Bool{}
		m.running[src] = &runningDev{cancel: dcancel, params: want, done: done, up: up}
		go func(src string, p scanParams, up *atomic.Bool) {
			defer close(done)
			superviseSDR(dctx, m.d, src, p, up)
		}(src, want, up)
	}
}

func (m *manager) stopAll() {
	for src, rd := range m.running {
		rd.cancel()
		<-rd.done
		delete(m.running, src)
	}
}

// listenConfig opens a dedicated LISTEN connection and signals on winnow_config.
func (m *manager) listenConfig(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	go func() {
		for ctx.Err() == nil {
			conn, err := m.d.Pool().Acquire(ctx)
			if err != nil {
				time.Sleep(2 * time.Second)
				continue
			}
			if _, err := conn.Exec(ctx, "LISTEN winnow_config"); err != nil {
				conn.Release()
				time.Sleep(2 * time.Second)
				continue
			}
			for ctx.Err() == nil {
				if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
					break
				}
				select {
				case ch <- struct{}{}:
				default:
				}
			}
			conn.Release()
		}
	}()
	return ch
}

func mustDB(ctx context.Context) *db.DB {
	for attempt := 1; attempt <= 30; attempt++ {
		d, err := db.New(ctx, config.DatabaseURL())
		if err == nil {
			return d
		}
		log.Printf("[capture] waiting for db (attempt %d): %v", attempt, err)
		time.Sleep(2 * time.Second)
	}
	log.Fatal("[capture] database unreachable")
	return nil
}

// superviseSDR runs rtl_tcp + rtlamr for one device with the given scan params,
// restarting on failure until ctx is cancelled. rtl_tcp is addressed by the
// device's current index; readings are tagged with its stable source id.
func superviseSDR(ctx context.Context, d *db.DB, source string, p scanParams, up *atomic.Bool) {
	defer up.Store(false)
	port := 1234 + p.devIndex
	server := "127.0.0.1:" + itoa(port)
	devIdx := itoa(p.devIndex)
	ppm := p.ppm
	if ppm == "" {
		ppm = "0"
	}
	freq := p.freq
	if freq == "" {
		freq = "912600155"
	}
	msgtype := p.msgtype
	if msgtype == "" {
		msgtype = "scm,scm+,idm"
	}

	for ctx.Err() == nil {
		up.Store(false)
		log.Printf("[capture] source=%s (dev %s) starting rtl_tcp on %s (freq=%s gain=%s)", source, devIdx, server, freq, p.gain)
		tcpArgs := []string{"-d", devIdx, "-a", "127.0.0.1", "-p", itoa(port), "-P", ppm}
		if p.gain != "" {
			tcpArgs = append(tcpArgs, "-g", p.gain)
		}
		openLock.Lock()
		tcp := exec.CommandContext(ctx, "rtl_tcp", tcpArgs...)
		tcp.Stderr = prefixWriter("rtl_tcp:" + source)
		if err := tcp.Start(); err != nil {
			openLock.Unlock()
			log.Printf("[capture] source=%s rtl_tcp start: %v", source, err)
			if !sleepCtx(ctx, 5*time.Second) {
				return
			}
			continue
		}
		time.Sleep(2 * time.Second) // hold the open lock while rtl_tcp binds the device
		openLock.Unlock()

		amrArgs := []string{"-server=" + server, "-msgtype=" + msgtype, "-format=json", "-centerfreq=" + freq}
		if p.filt != "" {
			amrArgs = append(amrArgs, "-filterid="+p.filt)
		}
		amr := exec.CommandContext(ctx, "rtlamr", amrArgs...)
		amr.Stderr = prefixWriter("rtlamr:" + source)
		stdout, _ := amr.StdoutPipe()
		if err := amr.Start(); err != nil {
			log.Printf("[capture] source=%s rtlamr start: %v", source, err)
			_ = tcp.Process.Kill()
			if !sleepCtx(ctx, 5*time.Second) {
				return
			}
			continue
		}
		up.Store(true) // pipeline live; healthy even if the meters are quiet
		ingest(ctx, d, source, stdout)
		up.Store(false)

		_ = amr.Wait()
		_ = tcp.Process.Kill()
		_ = tcp.Wait()
		if ctx.Err() != nil {
			return
		}
		log.Printf("[capture] source=%s pipeline exited; restarting in 5s", source)
		if !sleepCtx(ctx, 5*time.Second) {
			return
		}
	}
}

// sleepCtx sleeps for d unless ctx is cancelled first; returns false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func superviseMock(ctx context.Context, d *db.DB, source string) {
	for ctx.Err() == nil {
		r, w := io.Pipe()
		go func() { mockStream(ctx, w); w.Close() }()
		ingest(ctx, d, source, r)
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

// ingest reads rtlamr JSON lines, stores them, and heartbeats every 25 rows.
func ingest(ctx context.Context, d *db.DB, source string, stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var n int64
	lastTS := time.Now().UTC()
	// Seed a heartbeat so the source shows up immediately (a near-silent dongle
	// then correctly goes stale rather than staying invisible until 25 packets).
	_ = d.UpdateHeartbeat(ctx, source, lastTS, n)
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		r, ok := ert.ExtractReading([]byte(line), source)
		if !ok {
			continue
		}
		if err := d.InsertReading(ctx, r, line); err != nil {
			log.Printf("[ingest:%s] insert error: %v", source, err)
			return // reset the pipeline
		}
		lastTS = r.TS
		n++
		if n%25 == 0 {
			_ = d.UpdateHeartbeat(ctx, source, lastTS, n)
		}
	}
	_ = d.UpdateHeartbeat(ctx, source, lastTS, n)
}

// --- helpers ----------------------------------------------------------------

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = []string{"0"}
	}
	return out
}
func itoa(n int) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

type pw struct{ tag string }

func prefixWriter(tag string) io.Writer { return &pw{tag} }
func (w *pw) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			log.Printf("[%s] %s", w.tag, line)
		}
	}
	return len(p), nil
}
