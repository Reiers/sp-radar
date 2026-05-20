// filcensusd is the receiver daemon that runs on the dashboard host
// (filcensus.reiers.io). Its only job:
//
//   1. Accept POSTed snapshots from the mainnet node at /_ingest (bearer auth)
//   2. Validate SHA256, network, schema version
//   3. Atomically write to <snapshot-dir>/<network>-<YYYY-MM-DD>.json plus
//      <network>-latest.json symlink
//   4. Re-render the static dashboard via internal/render
//
// No outbound traffic, no shell-out, no plugins. The mainnet node never reads
// from this daemon — strictly one-way push from node → daemon.
//
// Run as a non-privileged systemd unit, behind Caddy/Cloudflare for TLS.
// See deploy/filcensusd.service and deploy/Caddyfile.example for templates.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Reiers/sp-radar/internal/render"
	"github.com/Reiers/sp-radar/internal/snapshot"
)

var version = "0.1.0-dev"

func main() {
	addr := flag.String("addr", ":8443", "Listen address")
	snapDir := flag.String("snapshots", "/var/lib/filcensus/snapshots", "Snapshot output directory")
	siteDir := flag.String("site", "/var/lib/filcensus/site", "Static dashboard output directory")
	tokenFile := flag.String("token-file", "/etc/filcensus/push-token", "File holding the shared bearer token")
	maxSize := flag.Int64("max-bytes", 50*1024*1024, "Maximum upload size in bytes")
	flag.Parse()

	token, err := loadToken(*tokenFile)
	if err != nil {
		log.Fatalf("load token: %v", err)
	}
	if err := os.MkdirAll(*snapDir, 0755); err != nil {
		log.Fatalf("snapshots dir: %v", err)
	}
	if err := os.MkdirAll(*siteDir, 0755); err != nil {
		log.Fatalf("site dir: %v", err)
	}

	srv := &server{
		snapDir:  *snapDir,
		siteDir:  *siteDir,
		token:    token,
		maxBytes: *maxSize,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", srv.handleHealth)
	mux.HandleFunc("/_ingest", srv.handleIngest)
	mux.HandleFunc("/_status", srv.handleStatus)
	mux.HandleFunc("/api/upgrade/nv28.json", srv.handleUpgradeNV28)

	// On startup: render any latest snapshot we already have on disk so the
	// dashboard is correct even before the next ingest fires.
	if err := srv.renderLatest(); err != nil {
		log.Printf("startup render: %v (continuing)", err)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 * 1024,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		log.Printf("filcensusd %s listening on %s (snap=%s site=%s)", version, *addr, *snapDir, *siteDir)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutting down...")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	_ = httpSrv.Shutdown(shutCtx)
}

func loadToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(data))
	if len(tok) < 16 {
		return "", fmt.Errorf("token in %s is too short (<16 chars); generate with `openssl rand -hex 32`", path)
	}
	return tok, nil
}

type server struct {
	snapDir  string
	siteDir  string
	token    string
	maxBytes int64

	// renderMu serialises rendering so two near-simultaneous uploads don't
	// stomp each other.
	renderMu sync.Mutex

	// readiness cache + lock, used by handleUpgradeNV28 (upgrade.go) so we
	// don't re-parse the snapshot on every hit.
	readinessMu sync.Mutex
	readiness   *readinessCache
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, "ok\n")
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.snapDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type item struct {
		Name    string `json:"name"`
		Size    int64  `json:"size"`
		ModTime string `json:"mtime"`
	}
	var out []item
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, item{Name: e.Name(), Size: fi.Size(), ModTime: fi.ModTime().UTC().Format(time.RFC3339)})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version":   version,
		"snapshots": out,
	})
}

func (s *server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Constant-time auth comparison.
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(auth), []byte(s.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBytes)

	if err := r.ParseMultipartForm(s.maxBytes); err != nil {
		http.Error(w, "parse multipart: "+err.Error(), http.StatusBadRequest)
		return
	}
	declared := r.FormValue("sha256")
	network := r.FormValue("network")
	if network == "" {
		http.Error(w, "missing network field", http.StatusBadRequest)
		return
	}
	if network != "mainnet" && network != "calibration" {
		http.Error(w, "unsupported network", http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("snapshot")
	if err != nil {
		http.Error(w, "missing snapshot file: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if int64(len(body)) > s.maxBytes {
		http.Error(w, "too big", http.StatusRequestEntityTooLarge)
		return
	}

	digest := sha256.Sum256(body)
	digestHex := hex.EncodeToString(digest[:])
	if declared != "" && declared != digestHex {
		http.Error(w, "sha256 mismatch", http.StatusBadRequest)
		return
	}

	// Validate it's a real snapshot before writing to disk.
	var snap snapshot.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		http.Error(w, "not a valid snapshot: "+err.Error(), http.StatusBadRequest)
		return
	}
	if snap.SchemaVersion == 0 || snap.Network == "" {
		http.Error(w, "snapshot missing schema_version or network", http.StatusBadRequest)
		return
	}
	if snap.Network != network {
		http.Error(w, "metadata network does not match snapshot network", http.StatusBadRequest)
		return
	}

	dateStamp := snap.GeneratedAt.UTC().Format("2006-01-02")
	finalName := fmt.Sprintf("%s-%s.json", snap.Network, dateStamp)
	finalPath := filepath.Join(s.snapDir, finalName)
	tmpPath := finalPath + ".tmp"
	if err := os.WriteFile(tmpPath, body, 0644); err != nil {
		http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		http.Error(w, "rename: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Update <network>-latest.json symlink atomically.
	latestLink := filepath.Join(s.snapDir, snap.Network+"-latest.json")
	tmpLink := latestLink + ".tmp"
	_ = os.Remove(tmpLink)
	if err := os.Symlink(finalName, tmpLink); err != nil {
		log.Printf("symlink: %v", err)
	} else {
		_ = os.Rename(tmpLink, latestLink)
	}

	// Re-render the dashboard. Failure is non-fatal — the JSON is already saved.
	if err := s.renderLatest(); err != nil {
		log.Printf("re-render: %v", err)
	}

	resp := map[string]string{
		"stored": "true",
		"path":   finalName,
		"sha256": digestHex,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
	log.Printf("ingested %s (%d bytes, %s)", finalName, len(body), digestHex[:12])
}

// renderLatest finds the most recent mainnet snapshot in snapDir and renders
// it to siteDir. Caller may invoke from startup or on every ingest.
func (s *server) renderLatest() error {
	s.renderMu.Lock()
	defer s.renderMu.Unlock()

	// Prefer mainnet-latest.json if it exists, else most-recent mainnet-*.
	candidate := filepath.Join(s.snapDir, "mainnet-latest.json")
	if _, err := os.Stat(candidate); err != nil {
		entries, err := os.ReadDir(s.snapDir)
		if err != nil {
			return err
		}
		var newest os.DirEntry
		var newestTime time.Time
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), "mainnet-") {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			if fi.ModTime().After(newestTime) {
				newest = e
				newestTime = fi.ModTime()
			}
		}
		if newest == nil {
			return errors.New("no mainnet snapshot to render yet")
		}
		candidate = filepath.Join(s.snapDir, newest.Name())
	}
	snap, err := snapshot.Read(candidate)
	if err != nil {
		return fmt.Errorf("read %s: %w", candidate, err)
	}
	if err := render.Render(snap, s.siteDir); err != nil {
		return err
	}
	log.Printf("rendered %s -> %s", filepath.Base(candidate), s.siteDir)
	return nil
}
