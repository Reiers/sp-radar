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
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/Reiers/sp-radar/internal/snapshot"
)
// We use text/template (not html/template) deliberately. The few helpers
// that return raw HTML (operatorRegions, focRegion country pills) are
// trusted: they only emit our own static markup with attribute values
// from a hard-coded palette map. No user-controlled string ever lands
// inside the spliced HTML.

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
// Keep in sync with the files in internal/render/assets/logos/.
//
// Real upstream marks for the four major projects (Lotus, Curio, Boost,
// Venus) come from the project repos / press kits, copied from
// ~/Desktop/Logos and used under fair-use citation. Smaller fallback marks
// are unique monograms generated below — each gets its own colour so the
// distribution rows visually separate.
var softwareLogos = map[string]string{
	"lotus":       "lotus.svg",
	"forest":      "forest.png",
	"venus":       "venus.png",
	"curio":       "curio.svg",
	"boost":       "boost.png",
	"lotus-miner": "lotus-miner.svg",
	"venus-miner": "venus-miner.svg",
	"droplet":     "droplet.svg",
	"markets":     "markets.svg",
	"private":     "private.svg",
	"no-peer-id":  "no-peer-id.svg",
	"other":       "other.svg",
	"unknown":     "unknown.svg",
}

// softwareDisplayLabel maps internal detect.Software identifiers to the
// human-friendly label shown on the dashboard. "unknown" is renamed to
// "custom software" since every reachable SP we couldn't parse is
// running *something* — just not a known agent string.
var softwareDisplayLabel = map[string]string{
	"unknown":     "custom software",
	"other":       "custom software",
	"private":     "private (firewalled)",
	"no-peer-id":  "no peer ID published",
	"lotus-miner": "lotus-miner (legacy)",
	"venus-miner": "venus-miner",
}

func softwareDisplayName(sw string) string {
	if n, ok := softwareDisplayLabel[sw]; ok {
		return n
	}
	return sw
}

// softwareDistSorted converts a sw→count map to a percentage-sorted slice.
// Exposed to templates as a func. Display name is humanised; the internal
// `Slug` is preserved for CSS class lookups (`sw-{slug}`).
type softwareEntryV2 struct {
	SoftwareEntry
	Slug string // internal sw name for CSS classes
}

func softwareDistSorted(m map[string]int, total int) []softwareEntryV2 {
	out := make([]softwareEntryV2, 0, len(m))
	for sw, c := range m {
		pct := 0.0
		if total > 0 {
			pct = float64(c) * 100.0 / float64(total)
		}
		logo := softwareLogos[sw]
		if logo == "" {
			logo = "unknown.svg"
		}
		out = append(out, softwareEntryV2{
			SoftwareEntry: SoftwareEntry{Name: softwareDisplayName(sw), Logo: logo, Count: c, Percent: pct},
			Slug:          sw,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// PageData wraps a Snapshot with derived fields the template needs but
// that don't make sense to persist. Computed once at render time.
type PageData struct {
	*snapshot.Snapshot

	// Concentration thresholds (smallest k such that top-k ops hold >= X%)
	NarrativeFiftyPctOps        int
	NarrativeSeventyFivePctOps  int
	NarrativeNinetyPctOps       int
	NarrativeNinetyFivePctOps   int
	NarrativeNinetyNinePctOps   int

	// Top-tier stacked-bar segments (top 1, 2-5, 6-10, 11-20, rest).
	OperatorTiers []TierSeg

	// QA / raw ratio (e.g. 8.7 means QA = 8.7 × raw)
	QARawRatio float64
	// Verified vs CC share of raw bytes
	VerifiedSharePct float64
	CCSharePct       float64

	// Geo distribution (top countries by reachable IP count)
	GeoTopCountries  []GeoSlice
	GeoTotalIPs      int
	GeoNarrativeTop2 string

	// Healthy FoC subset (HTTP 2xx/3xx) + count of hidden providers
	HealthyFoCNodes []snapshot.FoCNodeRecord
	FoCHiddenCount  int

	// Operators-table size (we render top N); exposed so the section
	// header can reference the actual number.
	OperatorsShown int

	// Declining miners: SPs whose Filfox rawBytePowerDelta is negative.
	// Sorted by largest absolute decline first. We surface the top N on
	// the dashboard and roll the rest into a footnote.
	DecliningMiners      []DeclineRow
	DecliningTotalCount  int     // total miners with negative delta
	DecliningTotalLossPiB float64 // aggregate raw-PiB loss across the decliners

	// Network sentiment meter (CMC fear/greed style, 0..100).
	Sentiment SentimentMeter

	// Power history points (raw + QA EiB over time).
	PowerHistory []HistPoint
}

// DeclineRow is one row in the Declining storage providers section.
type DeclineRow struct {
	MinerID    string
	CurrentPiB float64
	DeltaPiB   float64 // negative
	DeclinePct float64 // negative
	CountryCC  string  // for the region pill (best-effort)
}

// TierSeg is one segment of the top-tier stacked bar.
type TierSeg struct {
	Rank       string  // e.g. "Top 1", "Ranks 2-5"
	Share      float64 // own share of total power, 0..100
	Cumulative float64 // running total at end of this segment, 0..100
	WidthPct   float64 // CSS width allocation (with min-floor for legibility)
	Gradient   string  // CSS background gradient
}

// GeoSlice is one country slice for the donut + table.
type GeoSlice struct {
	CC        string  // ISO 2-letter
	Name      string  // "China"
	Count     int     // # of unique IPs
	Pct       float64 // share of geolocated IPs
	Color     string  // primary color (CSS)
	ColorDark string  // gradient endpoint color
	ArcPath   string  // SVG d= for the donut arc
}

// buildPageData computes all derived fields.
func buildPageData(s *snapshot.Snapshot) *PageData {
	pd := &PageData{Snapshot: s}
	pd.NarrativeFiftyPctOps = opsForPowerThreshold(s.Operators, 0.50)
	pd.NarrativeSeventyFivePctOps = opsForPowerThreshold(s.Operators, 0.75)
	pd.NarrativeNinetyPctOps = opsForPowerThreshold(s.Operators, 0.90)
	pd.NarrativeNinetyFivePctOps = opsForPowerThreshold(s.Operators, 0.95)
	pd.NarrativeNinetyNinePctOps = opsForPowerThreshold(s.Operators, 0.99)
	pd.OperatorTiers = buildTierSegments(s.Operators)
	pd.OperatorsShown = minInt(30, len(s.Operators))

	if s.NetworkTruth != nil {
		if s.NetworkTruth.RawPiB > 0 {
			pd.QARawRatio = s.NetworkTruth.QAPiB / s.NetworkTruth.RawPiB
		}
		if s.NetworkTruth.RawPiB > 0 {
			pd.VerifiedSharePct = 100 * s.NetworkTruth.VerifiedRawPiBEstimate / s.NetworkTruth.RawPiB
			pd.CCSharePct = 100 - pd.VerifiedSharePct
			if pd.CCSharePct < 0 {
				pd.CCSharePct = 0
			}
		}
	}

	pd.GeoTopCountries, pd.GeoTotalIPs = buildGeoSlices(s.SPs)
	pd.GeoNarrativeTop2 = geoNarrative(pd.GeoTopCountries)

	pd.HealthyFoCNodes, pd.FoCHiddenCount = filterHealthyFoC(s.FoCNodes)
	pd.DecliningMiners, pd.DecliningTotalCount, pd.DecliningTotalLossPiB = buildDecliningMiners(s.SPs, 20)
	pd.Sentiment = BuildSentimentMeter(s)
	pd.PowerHistory = PowerHistoryWithLatest(s)
	return pd
}

func minInt(a, b int) int { if a < b { return a }; return b }

// opsForPowerThreshold returns the smallest k such that the top-k operators
// (assumed sorted by power desc) hold >= frac of the total network power.
// Returns 0 if total is zero.
func opsForPowerThreshold(ops []snapshot.Operator, frac float64) int {
	total := new(big.Int)
	for _, op := range ops {
		if x, ok := new(big.Int).SetString(op.RawBytePower, 10); ok {
			total.Add(total, x)
		}
	}
	if total.Sign() == 0 {
		return 0
	}
	thresholdF := new(big.Float).Mul(new(big.Float).SetInt(total), big.NewFloat(frac))
	cum := new(big.Int)
	for i, op := range ops {
		if x, ok := new(big.Int).SetString(op.RawBytePower, 10); ok {
			cum.Add(cum, x)
		}
		cumF := new(big.Float).SetInt(cum)
		if cumF.Cmp(thresholdF) >= 0 {
			return i + 1
		}
	}
	return len(ops)
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
		"softwareDistSorted":   softwareDistSorted,
		"add":                  func(a, b int) int { return a + b },
		"sub":                  func(a, b float64) float64 { return a - b },
		"div":                  func(a, b float64) float64 { if b == 0 { return 0 }; return a / b },
		"topOperators":         topOperators,
		"powerPiB":             powerPiB,
		"powerPctOfNetwork":    powerPctOfNetwork,
		"topNStrings":          topNStrings,
		"commaInt":             commaInt,
		"commaFloat":           commaFloat,
		"powerHumanPiB":        powerHumanPiB,
		"opOwnerLabel":         opOwnerLabel,
		"lorenzPath":           lorenzPath,
		"operatorRegions":      operatorRegions,
		"operatorMemberSample": operatorMemberSample,
		"focRegion":            focRegion,
		"regionPillByCC":       regionPillByCC,
		"powerHistorySVG":      PowerHistorySVG,
		"sentimentArc":         SentimentMeterArc,
		"sentimentTickX":       SentimentMeterTickX,
		"sentimentTickY":       SentimentMeterTickY,
	}
	// Pre-populate the IP → CC lookup so operatorRegions can resolve.
	populateOperatorIPCC(snap)

	tpl, err := template.New("index").Funcs(funcs).Parse(string(tplBytes))
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}
	idx, err := os.Create(filepath.Join(outDir, "index.html"))
	if err != nil {
		return err
	}
	defer idx.Close()
	pd := buildPageData(snap)
	if err := tpl.Execute(idx, pd); err != nil {
		return fmt.Errorf("execute: %w", err)
	}

	return nil
}

// topOperators returns the first n operators (slice already sorted by power desc).
func topOperators(ops []snapshot.Operator, n int) []snapshot.Operator {
	if n <= 0 || n > len(ops) {
		return ops
	}
	return ops[:n]
}

// powerPiB formats a big-int decimal byte string as PiB with 1 decimal place.
func powerPiB(s string) string {
	x, ok := new(big.Int).SetString(s, 10)
	if !ok || x == nil {
		return "—"
	}
	fx := new(big.Float).SetInt(x)
	pib := new(big.Float).Quo(fx, big.NewFloat(1<<50))
	f, _ := pib.Float64()
	return fmt.Sprintf("%.1f", f)
}

// powerPctOfNetwork returns "X.XX%" of this row's power vs the sum of all
// operator powers. Computed in big-int arithmetic to avoid float drift on
// EiB-scale numbers.
func powerPctOfNetwork(rowPower string, all []snapshot.Operator) string {
	row, ok := new(big.Int).SetString(rowPower, 10)
	if !ok || row == nil {
		return "—"
	}
	total := new(big.Int)
	for _, op := range all {
		x, ok := new(big.Int).SetString(op.RawBytePower, 10)
		if !ok {
			continue
		}
		total.Add(total, x)
	}
	if total.Sign() == 0 {
		return "—"
	}
	num := new(big.Float).SetInt(row)
	den := new(big.Float).SetInt(total)
	pct, _ := new(big.Float).Quo(new(big.Float).Mul(num, big.NewFloat(100)), den).Float64()
	return fmt.Sprintf("%.2f%%", pct)
}

// powerHumanPiB formats a raw-bytes value as PiB or EiB depending on size.
// Threshold is 1024 PiB (= 1 EiB). Returns a 2-element string slice
// (val, unit) — Go templates can't multi-return non-error values, so we
// pack it in a slice and the template uses {{index $v 0}} / {{index $v 1}}.
func powerHumanPiB(piB float64) []string {
	if piB >= 1024 {
		return []string{fmt.Sprintf("%.1f", piB/1024), "EiB"}
	}
	return []string{commaFloat(piB, 0), "PiB"}
}

// opOwnerLabel returns a short description of the operator's owner addresses.
// One owner → the address. Multiple → "<first> +N more".
func opOwnerLabel(ops []string) string {
	switch len(ops) {
	case 0:
		return "—"
	case 1:
		return ops[0]
	case 2:
		return ops[0] + ", " + ops[1]
	default:
		return fmt.Sprintf("%s +%d more", ops[0], len(ops)-1)
	}
}

// lorenzPath produces an SVG path d= attribute for a cumulative-power
// (Lorenz-style) curve on a 1000×400 viewbox. X axis = operators sorted
// descending by power, Y axis = cumulative share of network power.
// We start at (0,400) and end at (1000, 0) (full coverage), with the
// curve hugging the upper-left for high concentration.
func lorenzPath(ops []snapshot.Operator) string {
	if len(ops) == 0 {
		return ""
	}
	total := new(big.Int)
	powers := make([]*big.Int, 0, len(ops))
	for _, op := range ops {
		x, ok := new(big.Int).SetString(op.RawBytePower, 10)
		if !ok || x == nil {
			x = new(big.Int)
		}
		powers = append(powers, x)
		total.Add(total, x)
	}
	if total.Sign() == 0 {
		return ""
	}
	totalF := new(big.Float).SetInt(total)
	n := len(ops)
	var b strings.Builder
	b.WriteString("M 0 400")
	cum := new(big.Int)
	for i, p := range powers {
		cum.Add(cum, p)
		x := float64(i+1) * (1000.0 / float64(n))
		cumF := new(big.Float).SetInt(cum)
		share, _ := new(big.Float).Quo(cumF, totalF).Float64()
		y := 400 - share*400
		b.WriteString(fmt.Sprintf(" L %.1f %.1f", x, y))
	}
	return b.String()
}

// commaInt formats an int64 with thousands separators (e.g. 1,234,567).
func commaInt(n int64) string {
	if n < 0 {
		return "-" + commaInt(-n)
	}
	s := fmt.Sprintf("%d", n)
	out := ""
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out += ","
		}
		out += string(c)
	}
	return out
}

// commaFloat formats a float with thousands separators and given decimal places.
func commaFloat(f float64, decimals int) string {
	if decimals == 0 {
		return commaInt(int64(f))
	}
	whole := int64(f)
	frac := f - float64(whole)
	if frac < 0 {
		frac = -frac
	}
	return fmt.Sprintf("%s.%0*d", commaInt(whole), decimals, int64(frac*pow10(decimals)+0.5))
}

func pow10(n int) float64 {
	x := 1.0
	for i := 0; i < n; i++ {
		x *= 10
	}
	return x
}

// topNStrings returns the first n elements of a string slice, joined with ", ".
// Used to surface a few "top owners" in the operator table without overwhelming.
func topNStrings(ss []string, n int) string {
	if n <= 0 || n >= len(ss) {
		return strings.Join(ss, ", ")
	}
	return strings.Join(ss[:n], ", ")
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
