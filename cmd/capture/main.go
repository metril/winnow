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
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"winnow/internal/config"
	"winnow/internal/db"
	"winnow/internal/ert"
	"winnow/internal/model"
)

func main() {
	mock := flag.Bool("mock", os.Getenv("CAPTURE_MOCK") == "1", "use synthetic data, no SDR")
	flag.Parse()
	log.SetFlags(log.LstdFlags)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Remote-agent mode: decode locally and stream to the main app over an
	// encrypted session. No DB access from the remote host.
	if os.Getenv("AGENT_URL") != "" {
		runAgent(ctx)
		return
	}

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

// Sink is the storage target for decoded readings. *db.DB satisfies it directly
// (local mode); the remote agent provides a WebSocket-backed implementation so
// the same supervise/ingest path serves both without knowing where data lands.
type Sink interface {
	InsertReading(ctx context.Context, r model.Reading, raw string) error
	UpdateHeartbeat(ctx context.Context, source string, lastTS time.Time, total int64) error
}

// scanParams is the effective tuning for one dongle. It's comparable so a change
// is detected with ==.
type scanParams struct {
	devIndex                       int
	freq, gain, ppm, msgtype, filt string
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

// bus is the single USB-open governor. librtlsdr enumerates the whole USB bus on
// every open and each open (rtl_tcp capture AND rtl_test enumeration) issues a
// libusb device reset — so reset cadence, not the dongle, decides whether a
// marginal device survives. ALL opens pass through here. It (a) serializes the
// open+settle window so concurrent receivers don't race ("connection refused" as
// rtl_tcp dies before binding) and (b) imposes one bus-wide escalating backoff so
// a device that fails fast is never reset faster than USB can recover. The serial
// window is a capacity-1 channel (not a Mutex) so acquisition is ctx-cancellable,
// keeping shutdown prompt even mid-backoff.
var bus = usbGate{sem: make(chan struct{}, 1)}

type usbGate struct {
	sem    chan struct{} // cap 1; held only across one open+settle window (~2s)
	bmu    sync.Mutex    // guards fails/nextOK
	fails  int
	nextOK time.Time // earliest the next open may begin
}

const (
	gateBase    = 30 * time.Second // first penalty; doubles per consecutive fast failure
	gateMax     = 5 * time.Minute  // cap — a struggling bus gets long rests
	gateHealthy = 60 * time.Second // a run/enumerate this good resets the penalty
	usbSettle   = 2 * time.Second  // pause after a USB open+reset before the next open
)

// begin waits out any bus-wide backoff, then takes the serial window. It
// sleeps-then-locks (never holds the window while waiting) and re-checks nextOK
// after acquiring, so one dongle's long backoff never blocks another's read.
// Returns false if ctx is cancelled while waiting — the caller must then NOT
// call end()/ok()/fail().
func (g *usbGate) begin(ctx context.Context) bool {
	for {
		g.bmu.Lock()
		wait := time.Until(g.nextOK)
		g.bmu.Unlock()
		if wait > 0 {
			log.Printf("[capture] usb gate: waiting %s before next open", wait.Round(time.Second))
			if !sleepCtx(ctx, wait) {
				return false
			}
		}
		select {
		case g.sem <- struct{}{}:
		case <-ctx.Done():
			return false
		}
		g.bmu.Lock()
		ready := !time.Now().Before(g.nextOK)
		g.bmu.Unlock()
		if ready {
			return true
		}
		<-g.sem // a fail() pushed nextOK out while we queued; release and re-wait
	}
}

// end releases the serial window.
func (g *usbGate) end() { <-g.sem }

// ok clears the penalty after a successful enumerate or healthy run.
func (g *usbGate) ok() {
	g.bmu.Lock()
	g.fails = 0
	g.nextOK = time.Now()
	g.bmu.Unlock()
}

// fail escalates the bus-wide backoff after a fast failure.
func (g *usbGate) fail() {
	g.bmu.Lock()
	g.fails++
	d := gateBase << (g.fails - 1)
	if d <= 0 || d > gateMax { // <=0 guards the shift overflowing
		d = gateMax
	}
	g.nextOK = time.Now().Add(d)
	g.bmu.Unlock()
	log.Printf("[capture] usb gate: backing off %s (%d consecutive fast failures)", d, g.fails)
}

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
func (m *manager) rescanNeeded() bool { return dongleRescanNeeded(m.running) }

// dongleRescanNeeded is the shared down-streak check used by both the local manager
// and the remote agent: true once any supervisor has been observed not-up for two
// consecutive health ticks (~60s). Mutates each runningDev's downStreak.
func dongleRescanNeeded(running map[string]*runningDev) bool {
	if len(running) == 0 {
		return false
	}
	bad := false
	for _, rd := range running {
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
		// No spacing here: enumerateRTL opens the bus through the gate, which
		// already paces retries (and backs off when the bus is struggling).
		log.Printf("[capture] no SDRs detected; retrying")
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
func superviseSDR(ctx context.Context, d Sink, source string, p scanParams, up *atomic.Bool) {
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

	// Each iteration opens the device through the bus gate, which paces opens with
	// a bus-wide escalating backoff (see usbGate) so a marginal dongle is never
	// reset faster than USB can recover. We judge a run healthy by how long it
	// stayed up: a sustained run resets the penalty, a fast exit escalates it.
	for ctx.Err() == nil {
		up.Store(false)
		if !bus.begin(ctx) { // waits out any backoff, then takes the serial window
			return
		}
		// Time the run from AFTER the gate opens the device — NOT before begin(),
		// or the backoff wait would count toward gateHealthy and a fast-failing
		// open would look "healthy" and wrongly reset the penalty.
		start := time.Now()
		log.Printf("[capture] source=%s (dev %s) starting rtl_tcp on %s (freq=%s gain=%s)", source, devIdx, server, freq, p.gain)
		tcpArgs := []string{"-d", devIdx, "-a", "127.0.0.1", "-p", itoa(port), "-P", ppm}
		if p.gain != "" {
			tcpArgs = append(tcpArgs, "-g", p.gain)
		}
		tcp := exec.CommandContext(ctx, "rtl_tcp", tcpArgs...)
		tcp.Stderr = prefixWriter("rtl_tcp:" + source)
		if err := tcp.Start(); err != nil {
			log.Printf("[capture] source=%s rtl_tcp start: %v", source, err)
			bus.fail()
			bus.end()
			continue
		}
		// Hold the serial window across the open (the USB-contended part), then wait
		// for rtl_tcp to actually listen before releasing it and starting rtlamr.
		// rtl_tcp binds its socket only after rtlsdr_open completes (~1-2s), so a
		// blind sleep raced rtlamr's single no-retry connect against the bind: a
		// "connection refused" exit then killed rtl_tcp mid-open, looping forever.
		ready := waitListening(ctx, server, 12*time.Second)
		bus.end()
		if !ready {
			_ = tcp.Process.Kill()
			_ = tcp.Wait()
			if ctx.Err() != nil {
				return
			}
			log.Printf("[capture] source=%s rtl_tcp never listened on %s; restarting", source, server)
			bus.fail()
			continue
		}

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
			bus.fail()
			continue
		}
		up.Store(true) // pipeline live; healthy even if the meters are quiet
		ingest(ctx, d, source, stdout)
		up.Store(false)

		_ = amr.Wait()
		_ = tcp.Process.Kill()
		_ = tcp.Wait()
		if ctx.Err() != nil {
			return // a cancel (shutdown/config change) is neutral to the gate
		}
		if time.Since(start) >= gateHealthy {
			bus.ok() // came up and stayed up — clear the penalty
		} else {
			bus.fail() // never really came up — escalate the bus backoff
		}
		log.Printf("[capture] source=%s pipeline exited; will reopen via gate", source)
	}
}

// waitListening returns true once something accepts a TCP connection on addr
// (i.e. rtl_tcp finished opening+tuning the dongle and bound its socket), or
// false on ctx-cancel / deadline. This replaces a blind sleep: rtl_tcp binds
// only after rtlsdr_open completes, so polling the port is what tells us rtlamr
// can safely connect — and stops us from killing rtl_tcp mid-open.
func waitListening(ctx context.Context, addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return false
		}
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			c.Close()
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		if !sleepCtx(ctx, 200*time.Millisecond) {
			return false
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

func superviseMock(ctx context.Context, d Sink, source string) {
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
func ingest(ctx context.Context, d Sink, source string, stdout io.Reader) {
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
