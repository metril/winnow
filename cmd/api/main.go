// Command api serves winnow's JSON API + Server-Sent Events and the embedded
// React SPA. Reads are stateless against TimescaleDB; live updates are pushed
// via SSE fed by Postgres LISTEN (no polling).
package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"winnow/internal/config"
	"winnow/internal/db"
)

//go:embed all:dist
var distFS embed.FS

type server struct {
	d      *db.DB
	broker *broker
}

func main() {
	log.SetFlags(log.LstdFlags)
	ctx := context.Background()

	d := mustDB(ctx)
	defer d.Close()
	if err := d.InitSchema(ctx); err != nil {
		log.Printf("[api] schema init: %v", err)
	}

	s := &server{d: d, broker: newBroker()}
	go s.broker.run(ctx, d) // LISTEN → SSE fan-out

	mux := http.NewServeMux()
	s.routes(mux)

	addr := ":8000"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	log.Printf("[api] listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func mustDB(ctx context.Context) *db.DB {
	for attempt := 1; attempt <= 30; attempt++ {
		d, err := db.New(ctx, config.DatabaseURL())
		if err == nil {
			return d
		}
		log.Printf("[api] waiting for db (%d): %v", attempt, err)
		time.Sleep(2 * time.Second)
	}
	log.Fatal("[api] database unreachable")
	return nil
}

func (s *server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/stream", s.handleStream)

	mux.HandleFunc("GET /api/meters", s.handleMeters)
	mux.HandleFunc("GET /api/meters/{id}", s.handleMeterDetail)
	mux.HandleFunc("PATCH /api/meters/{id}", s.handleMeterPatch)
	mux.HandleFunc("DELETE /api/meters/{id}", s.handleDeleteMeter)
	mux.HandleFunc("GET /api/meters/{id}/filter-command", s.handleFilterCommand)
	mux.HandleFunc("GET /api/meters/{id}/export.csv", s.handleExportCSV)
	mux.HandleFunc("GET /api/meters/{id}/profile", s.handleProfile)
	mux.HandleFunc("GET /api/meters/{id}/benchmark", s.handleBenchmark)
	mux.HandleFunc("GET /api/series", s.handleSeries)

	mux.HandleFunc("GET /api/overview", s.handleOverview)
	mux.HandleFunc("GET /api/anomalies", s.handleAnomalies)

	mux.HandleFunc("GET /api/admin/stats", s.handleAdminStats)
	mux.HandleFunc("POST /api/admin/maintenance", s.handleMaintenance)
	mux.HandleFunc("POST /api/admin/delete", s.handleAdminDelete)

	mux.HandleFunc("GET /api/diagnostics/coverage", s.handleCoverage)
	mux.HandleFunc("GET /api/diagnostics/sources", s.handleSourceTimeline)

	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	mux.HandleFunc("POST /api/integrations/test", s.handleIntegrationsTest)
	mux.HandleFunc("GET /api/integrations/status", s.handleIntegrationsStatus)
	mux.HandleFunc("GET /api/ha/power-entities", s.handlePowerEntities)
	mux.HandleFunc("POST /api/ha/create-helper", s.handleCreateHelper)

	mux.HandleFunc("GET /api/devices", s.handleDevices)
	mux.HandleFunc("PUT /api/devices/{serial}", s.handlePutDevice)

	mux.HandleFunc("GET /api/identify", s.handleIdentify)
	mux.HandleFunc("GET /api/identify/auto", s.handleIdentifyAuto)
	mux.HandleFunc("GET /api/reference/series", s.handleReferenceSeries)

	mux.HandleFunc("GET /api/tests", s.handleListTests)
	mux.HandleFunc("POST /api/tests", s.handleCreateTest)
	mux.HandleFunc("POST /api/tests/start", s.handleStartTest)
	mux.HandleFunc("POST /api/tests/{id}/stop", s.handleStopTest)
	mux.HandleFunc("DELETE /api/tests/{id}", s.handleDeleteTest)
	mux.HandleFunc("GET /api/tests/combined", s.handleCombined)
	mux.HandleFunc("GET /api/tests/{id}/correlation", s.handleTestCorrelation)

	// SPA (embedded). Serves files; falls back to index.html for client routes.
	sub, _ := fs.Sub(distFS, "dist")
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(sub, trimSlash(r.URL.Path)); err != nil && r.URL.Path != "/" {
			r.URL.Path = "/" // SPA fallback
		}
		fileServer.ServeHTTP(w, r)
	})
}

func trimSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}
