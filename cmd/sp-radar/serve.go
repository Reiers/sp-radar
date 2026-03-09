package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/Reiers/sp-radar/internal/scanner"
	"github.com/urfave/cli/v2"
)

//go:embed dashboard/*
var dashFS embed.FS

var serveCmd = &cli.Command{
	Name:  "serve",
	Usage: "Run the SP Radar dashboard with periodic scanning",
	Flags: []cli.Flag{
		&cli.IntFlag{Name: "port", Value: 8080, Usage: "HTTP port"},
		&cli.DurationFlag{Name: "interval", Value: 6 * time.Hour, Usage: "Scan interval"},
		&cli.IntFlag{Name: "concurrency", Aliases: []string{"c"}, Value: 50},
		&cli.IntFlag{Name: "lotus-concurrency", Value: 50},
		&cli.DurationFlag{Name: "timeout", Value: 10 * time.Second},
		&cli.StringFlag{Name: "api", EnvVars: []string{"FULLNODE_API_INFO"}},
	},
	Action: func(cctx *cli.Context) error {
		api := cctx.String("api")
		if api == "" {
			return fmt.Errorf("set FULLNODE_API_INFO or use --api")
		}
		ctx, cancel := context.WithCancel(cctx.Context)
		defer cancel()
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		go func() { <-ch; cancel() }()

		home, _ := os.UserHomeDir()
		hdir := filepath.Join(home, ".sp-radar", "history")
		os.MkdirAll(hdir, 0755)

		s := &srv{
			opts: scanner.ScanOptions{
				APIInfo: api, Concurrency: cctx.Int("concurrency"),
				LotusConcurrency: cctx.Int("lotus-concurrency"), Timeout: cctx.Duration("timeout"),
			},
			hdir: hdir,
		}
		fmt.Fprintf(os.Stderr, "Initial scan...\n")
		s.scan(ctx)

		go func() {
			t := time.NewTicker(cctx.Duration("interval"))
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					s.scan(ctx)
				}
			}
		}()

		mux := http.NewServeMux()
		mux.HandleFunc("/api/latest", s.apiLatest)
		mux.HandleFunc("/api/history", s.apiHistory)
		sub, _ := fs.Sub(dashFS, "dashboard")
		mux.Handle("/", http.FileServer(http.FS(sub)))

		port := cctx.Int("port")
		fmt.Fprintf(os.Stderr, "Dashboard: http://localhost:%d\n", port)
		hs := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux}
		go func() { <-ctx.Done(); hs.Close() }()
		return hs.ListenAndServe()
	},
}

type srv struct {
	opts    scanner.ScanOptions
	hdir    string
	mu      sync.RWMutex
	latest  *scanner.ScanResult
	history []*scanner.ScanResult
}

func (s *srv) scan(ctx context.Context) {
	r, err := scanner.Scan(ctx, s.opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan error: %v\n", err)
		return
	}
	s.mu.Lock()
	s.latest = r
	s.history = append(s.history, r)
	s.mu.Unlock()
	p := filepath.Join(s.hdir, r.Timestamp.Format("2006-01-02T15-04-05")+".json")
	if f, err := os.Create(p); err == nil {
		json.NewEncoder(f).Encode(r)
		f.Close()
	}
}

func (s *srv) apiLatest(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if s.latest == nil {
		w.WriteHeader(503)
		return
	}
	json.NewEncoder(w).Encode(s.latest)
}

func (s *srv) apiHistory(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(s.history)
}
