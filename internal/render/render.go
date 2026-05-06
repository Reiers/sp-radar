// Package render produces the static dashboard from a Snapshot.
//
// The dashboard is a self-contained directory:
//
//   <out>/index.html
//   <out>/assets/css/site.css
//   <out>/assets/logos/*.svg|.png
//   <out>/data/<network>-<YYYY-MM-DD>.json   (a copy of the snapshot)
//
// Templates and CSS are embedded into the binary via embed.FS so the
// renderer has zero filesystem dependencies once compiled.
package render

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/Reiers/sp-radar/internal/snapshot"
)

//go:embed all:templates all:assets
var embedded embed.FS

// SoftwareEntry is a row in the per-software distribution table.
type SoftwareEntry struct {
	Name    string
	Logo    string // basename of file under assets/logos
	Count   int
	Percent float64
}

// softwareLogos maps detect.Software string identifiers → logo basename.
// Keep in sync with the files in web/assets/logos/.
var softwareLogos = map[string]string{
	"lotus":       "lotus.svg",
	"forest":      "forest.png",
	"venus":       "venus.svg",
	"curio":       "curio.svg",
	"boost":       "boost.svg",
	"lotus-miner": "lotus-miner.svg",
	"venus-miner": "venus-miner.svg",
	"droplet":     "droplet.svg",
	"markets":     "markets.svg",
	"unknown":     "unknown.svg",
}

// softwareDistSorted converts a sw→count map to a percentage-sorted slice.
// Exposed to templates as a func.
func softwareDistSorted(m map[string]int, total int) []SoftwareEntry {
	out := make([]SoftwareEntry, 0, len(m))
	for sw, c := range m {
		pct := 0.0
		if total > 0 {
			pct = float64(c) * 100.0 / float64(total)
		}
		logo := softwareLogos[sw]
		if logo == "" {
			logo = "unknown.svg"
		}
		out = append(out, SoftwareEntry{Name: sw, Logo: logo, Count: c, Percent: pct})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Render produces the static dashboard at outDir from snap.
func Render(snap *snapshot.Snapshot, outDir string) error {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// 1. Copy embedded assets (css, logos)
	if err := copyEmbeddedDir("assets", filepath.Join(outDir, "assets")); err != nil {
		return fmt.Errorf("copy assets: %w", err)
	}

	// 2. Render index.html
	tplBytes, err := embedded.ReadFile("templates/index.html")
	if err != nil {
		return fmt.Errorf("read template: %w", err)
	}
	funcs := template.FuncMap{
		"softwareDistSorted": softwareDistSorted,
	}
	tpl, err := template.New("index").Funcs(funcs).Parse(string(tplBytes))
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}
	idx, err := os.Create(filepath.Join(outDir, "index.html"))
	if err != nil {
		return err
	}
	defer idx.Close()
	if err := tpl.Execute(idx, snap); err != nil {
		return fmt.Errorf("execute: %w", err)
	}

	return nil
}

func copyEmbeddedDir(srcRoot, dstRoot string) error {
	return fs.WalkDir(embedded, srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(path, srcRoot)
		rel = strings.TrimPrefix(rel, "/")
		dst := filepath.Join(dstRoot, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		src, err := embedded.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, src)
		return err
	})
}
