package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// sdrDevice is a detected RTL-SDR dongle. `index` is its current librtlsdr index
// (passed to rtl_tcp -d); `source` is a STABLE id used to tag readings — the
// serial when unique, so it survives USB reordering across reboots.
type sdrDevice struct {
	index  int
	serial string
	name   string
	source string
}

var (
	foundRe   = regexp.MustCompile(`Found (\d+) device`)
	devLineRe = regexp.MustCompile(`^\s*(\d+):\s+(.*?),\s+(.*?),\s+SN:\s+(.*)$`)
)

// enumerateRTL lists connected RTL-SDR dongles. `rtl_test` prints the full
// device list before it tries to open device 0, so we read that list and stop
// as soon as we've seen the reported count (then it's killed by the timeout).
func enumerateRTL(ctx context.Context) []sdrDevice {
	c, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "rtl_test")
	out, err := cmd.StderrPipe()
	if err != nil {
		return nil
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[capture] rtl_test not available: %v", err)
		return nil
	}
	var devs []sdrDevice
	want := -1
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		line := sc.Text()
		if m := foundRe.FindStringSubmatch(line); m != nil {
			want, _ = strconv.Atoi(m[1])
		}
		if m := devLineRe.FindStringSubmatch(line); m != nil {
			idx, _ := strconv.Atoi(m[1])
			devs = append(devs, sdrDevice{index: idx, name: strings.TrimSpace(m[3]), serial: strings.TrimSpace(m[4])})
		}
		if want >= 0 && len(devs) >= want {
			break
		}
	}
	cancel()
	_ = cmd.Wait()
	assignSources(devs)
	return devs
}

// assignSources gives each dongle a stable source id: its serial when unique,
// otherwise the index (with a hint to set a unique serial via rtl_eeprom).
func assignSources(devs []sdrDevice) {
	counts := map[string]int{}
	for _, d := range devs {
		counts[d.serial]++
	}
	for i := range devs {
		if s := devs[i].serial; s != "" && counts[s] == 1 {
			devs[i].source = s
		} else {
			devs[i].source = fmt.Sprintf("dev%d", devs[i].index)
			log.Printf("[capture] device %d has an empty/duplicate serial (%q); tagging it %q. "+
				"For an id that's stable across reboots, set a unique serial: rtl_eeprom -d %d -s <name>",
				devs[i].index, devs[i].serial, devs[i].source, devs[i].index)
		}
	}
}

func describe(devs []sdrDevice) string {
	var parts []string
	for _, d := range devs {
		parts = append(parts, fmt.Sprintf("[%d] %s (%s)", d.index, d.name, d.source))
	}
	return strings.Join(parts, ", ")
}

// resolveDevices returns the dongles to capture. RTL_DEVICES=auto (the default)
// auto-detects every connected RTL-SDR; an explicit comma list of indices is
// still honored for back-compat.
func resolveDevices(ctx context.Context) []sdrDevice {
	raw := strings.TrimSpace(env("RTL_DEVICES", "auto"))
	if raw == "" || strings.EqualFold(raw, "auto") {
		for ctx.Err() == nil {
			if devs := enumerateRTL(ctx); len(devs) > 0 {
				log.Printf("[capture] auto-detected %d SDR(s): %s", len(devs), describe(devs))
				return devs
			}
			log.Printf("[capture] no SDRs detected; retrying in 5s")
			time.Sleep(5 * time.Second)
		}
		return nil
	}
	var devs []sdrDevice
	for _, tok := range splitCSV(raw) {
		idx, _ := strconv.Atoi(tok)
		devs = append(devs, sdrDevice{index: idx, source: tok})
	}
	log.Printf("[capture] using explicit RTL_DEVICES=%s", raw)
	return devs
}
