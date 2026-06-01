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
	cancel context.CancelFunc
	params scanParams
	done   chan struct{}
}

type manager struct {
	d         *db.DB
	running   map[string]*runningDev
	inventory []sdrDevice // enumerated once, while devices are free
}

// run enumerates dongles once (rtl_test needs exclusive access, so we can't
// re-run it while receivers hold the devices), then reconciles on startup, on
// config NOTIFYs, and on a periodic tick — all against the cached inventory.
func (m *manager) run(ctx context.Context) {
	notify := m.listenConfig(ctx)
	m.inventory = m.enumerateOnce(ctx)
	m.reconcile(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-ticker.C:
			m.reconcile(ctx)
		case <-notify:
			log.Printf("[capture] config changed; reconciling devices")
			m.reconcile(ctx)
		}
	}
}

// enumerateOnce probes for dongles (retrying until some appear) and records the
// inventory. Done before any receiver starts, so serials read reliably.
func (m *manager) enumerateOnce(ctx context.Context) []sdrDevice {
	for ctx.Err() == nil {
		devs := enumerateRTL(ctx)
		if len(devs) > 0 {
			log.Printf("[capture] detected %d SDR(s): %s", len(devs), describe(devs))
			for _, dev := range devs {
				_ = m.d.UpsertDevice(ctx, dev.source, dev.index, dev.name, dev.tuner)
			}
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
func (m *manager) reconcile(ctx context.Context) {
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
			freq:     cfg.Capture.Freq,
			gain:     cfg.Capture.DeviceGain(dev.source),
			ppm:      cfg.Capture.PPM,
			msgtype:  cfg.Capture.MsgType,
			filt:     cfg.Capture.FilterID,
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
		m.running[src] = &runningDev{cancel: dcancel, params: want, done: done}
		go func(src string, p scanParams) {
			defer close(done)
			superviseSDR(dctx, m.d, src, p)
		}(src, want)
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
func superviseSDR(ctx context.Context, d *db.DB, source string, p scanParams) {
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
		log.Printf("[capture] source=%s (dev %s) starting rtl_tcp on %s (freq=%s gain=%s)", source, devIdx, server, freq, p.gain)
		tcpArgs := []string{"-d", devIdx, "-a", "127.0.0.1", "-p", itoa(port), "-P", ppm}
		if p.gain != "" {
			tcpArgs = append(tcpArgs, "-g", p.gain)
		}
		tcp := exec.CommandContext(ctx, "rtl_tcp", tcpArgs...)
		tcp.Stderr = prefixWriter("rtl_tcp:" + source)
		if err := tcp.Start(); err != nil {
			log.Printf("[capture] source=%s rtl_tcp start: %v", source, err)
			if !sleepCtx(ctx, 5*time.Second) {
				return
			}
			continue
		}
		time.Sleep(2 * time.Second) // let rtl_tcp bind the device

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
		ingest(ctx, d, source, stdout)

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
