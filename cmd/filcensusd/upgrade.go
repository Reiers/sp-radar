// Package main: nv28 upgrade-readiness endpoint.
//
// GET /api/upgrade/nv28.json
//
// Reads the latest mainnet snapshot from disk, runs the readiness classifier,
// and serves the result as JSON with permissive CORS so any dashboard (calix
// included) can cross-fetch.
//
// The classifier is pure logic (internal/upgrade), so this handler is just
// snapshot-load + classify + serialize.
package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Reiers/sp-radar/internal/snapshot"
	"github.com/Reiers/sp-radar/internal/upgrade"
)

// readinessCache holds the last classified result so we don't re-parse the
// snapshot for every hit. Invalidated when the underlying file's mtime
// changes.
type readinessCache struct {
	mtime    time.Time
	result   upgrade.Readiness
	rendered []byte // pre-serialized JSON
}

func (s *server) handleUpgradeNV28(w http.ResponseWriter, r *http.Request) {
	// Permissive CORS so calix-mainnet (or any other downstream) can cross-fetch.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=60")

	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// We resolve via the symlink that the ingest pipeline maintains
	// (snapDir/mainnet-latest.json -> mainnet-YYYY-MM-DD.json).
	latestPath := filepath.Join(s.snapDir, "mainnet-latest.json")
	st, err := os.Stat(latestPath)
	if err != nil {
		http.Error(w, "no snapshot available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	s.readinessMu.Lock()
	defer s.readinessMu.Unlock()
	if s.readiness != nil && s.readiness.mtime.Equal(st.ModTime()) && len(s.readiness.rendered) > 0 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.readiness.rendered)
		return
	}

	// (Re)classify from disk.
	b, err := os.ReadFile(latestPath)
	if err != nil {
		http.Error(w, "read snapshot: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var snap snapshot.Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		http.Error(w, "parse snapshot: "+err.Error(), http.StatusInternalServerError)
		return
	}
	res := upgrade.Classify(&snap, upgrade.NV28FireHorse)
	rendered, err := json.Marshal(res)
	if err != nil {
		http.Error(w, "marshal: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.readiness = &readinessCache{
		mtime:    st.ModTime(),
		result:   res,
		rendered: rendered,
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(rendered)
}
