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

	var wg sync.WaitGroup
	if *mock {
		for _, src := range []string{"mock-0", "mock-1"} {
			wg.Add(1)
			go func(s string) { defer wg.Done(); superviseMock(ctx, database, s) }(src)
		}
	} else {
		for i, dev := range resolveDevices(ctx) {
			wg.Add(1)
			go func(dev sdrDevice, port int) { defer wg.Done(); superviseSDR(ctx, database, dev, port) }(dev, 1234+i)
		}
	}
	wg.Wait()
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

// superviseSDR runs rtl_tcp + rtlamr for one device, restarting on failure.
// rtl_tcp is addressed by the device's current index; readings are tagged with
// its stable source id (serial when unique).
func superviseSDR(ctx context.Context, d *db.DB, dev sdrDevice, port int) {
	freq := env("FREQ", "912600155")
	msgtype := env("RTLAMR_MSGTYPE", "scm,scm+,idm")
	filterID := env("RTLAMR_FILTERID", "")
	gain := env("GAIN", "")
	ppm := env("PPM", "0")
	server := "127.0.0.1:" + itoa(port)
	devIdx := itoa(dev.index)

	for ctx.Err() == nil {
		log.Printf("[capture] source=%s (dev %s) starting rtl_tcp on %s", dev.source, devIdx, server)
		tcpArgs := []string{"-d", devIdx, "-a", "127.0.0.1", "-p", itoa(port), "-P", ppm}
		if gain != "" {
			tcpArgs = append(tcpArgs, "-g", gain)
		}
		tcp := exec.CommandContext(ctx, "rtl_tcp", tcpArgs...)
		tcp.Stderr = prefixWriter("rtl_tcp:" + dev.source)
		if err := tcp.Start(); err != nil {
			log.Printf("[capture] source=%s rtl_tcp start: %v", dev.source, err)
			time.Sleep(5 * time.Second)
			continue
		}
		time.Sleep(2 * time.Second) // let rtl_tcp bind the device

		amrArgs := []string{"-server=" + server, "-msgtype=" + msgtype, "-format=json", "-centerfreq=" + freq}
		if filterID != "" {
			amrArgs = append(amrArgs, "-filterid="+filterID)
		}
		amr := exec.CommandContext(ctx, "rtlamr", amrArgs...)
		amr.Stderr = prefixWriter("rtlamr:" + dev.source)
		stdout, _ := amr.StdoutPipe()
		if err := amr.Start(); err != nil {
			log.Printf("[capture] source=%s rtlamr start: %v", dev.source, err)
			_ = tcp.Process.Kill()
			time.Sleep(5 * time.Second)
			continue
		}
		ingest(ctx, d, dev.source, stdout)

		_ = amr.Wait()
		_ = tcp.Process.Kill()
		_ = tcp.Wait()
		log.Printf("[capture] source=%s pipeline exited; restarting in 5s", dev.source)
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
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
