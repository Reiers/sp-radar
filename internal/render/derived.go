package render

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"

	"github.com/Reiers/sp-radar/internal/snapshot"
)

// --- TIER SEGMENTS (top-N stacked bar) ---

// buildTierSegments groups operators into the classic Pareto tiers
// (top 1, 2-5, 6-10, 11-20, rest) and produces stacked-bar segments
// labelled with cumulative share, mirroring the SP census report style.
func buildTierSegments(ops []snapshot.Operator) []TierSeg {
	if len(ops) == 0 {
		return nil
	}
	type slot struct {
		from, to int
		grad     string
		name     string
	}
	slots := []slot{
		{0, 1, "linear-gradient(90deg, #F85149, #DB61A2)", "Top 1"},
		{1, 5, "linear-gradient(90deg, #DB61A2, #A371F7)", "+2-5"},
		{5, 10, "linear-gradient(90deg, #A371F7, #58A6FF)", "+6-10"},
		{10, 20, "linear-gradient(90deg, #58A6FF, #3FB950)", "+11-20"},
		{20, len(ops), "var(--surface2)", "Rest"},
	}
	total := new(big.Int)
	for _, op := range ops {
		if x, ok := new(big.Int).SetString(op.RawBytePower, 10); ok {
			total.Add(total, x)
		}
	}
	if total.Sign() == 0 {
		return nil
	}
	totalF := new(big.Float).SetInt(total)
	var segs []TierSeg
	for _, s := range slots {
		if s.from >= len(ops) {
			break
		}
		end := s.to
		if end > len(ops) {
			end = len(ops)
		}
		sum := new(big.Int)
		for i := s.from; i < end; i++ {
			if x, ok := new(big.Int).SetString(ops[i].RawBytePower, 10); ok {
				sum.Add(sum, x)
			}
		}
		share, _ := new(big.Float).Quo(new(big.Float).SetInt(sum), totalF).Float64()
		segs = append(segs, TierSeg{
			Label:    fmt.Sprintf("%s: %.0f%%", s.name, share*100),
			WidthPct: math.Max(share*100, 4), // floor so very-thin segments stay readable
			Gradient: s.grad,
		})
	}
	// Re-normalise widths to sum to 100 so the bar fills cleanly even after the floor.
	var sumW float64
	for _, s := range segs {
		sumW += s.WidthPct
	}
	if sumW > 0 {
		for i := range segs {
			segs[i].WidthPct = segs[i].WidthPct * 100 / sumW
		}
	}
	return segs
}

// --- GEOGRAPHY ---

// countryColor returns the categorical color for a country code, matching
// the report's palette. Unknown codes fall back to the muted gray.
var countryPalette = map[string][2]string{
	"CN": {"#F85149", "#A8281F"},
	"HK": {"#DB61A2", "#A23873"},
	"US": {"#58A6FF", "#2C68C4"},
	"SG": {"#3FB950", "#268F2F"},
	"DE": {"#D29922", "#8C6716"},
	"VN": {"#A371F7", "#6B41C4"},
	"KR": {"#DB6D28", "#9C4716"},
	"JP": {"#5fa8d3", "#2c6e8f"},
	"GB": {"#2E86DE", "#1B4F88"},
	"FR": {"#22A6B3", "#13606A"},
	"CA": {"#F368E0", "#9C2C8C"},
	"NL": {"#FFA502", "#9C5C00"},
	"AU": {"#16A085", "#0C5C4B"},
	"SE": {"#3742FA", "#1E2496"},
	"CH": {"#E84118", "#8C2008"},
	"BR": {"#26C281", "#15704C"},
	"IN": {"#F39C12", "#8C5800"},
}

var countryName = map[string]string{
	"CN": "China", "HK": "Hong Kong", "US": "United States", "SG": "Singapore",
	"DE": "Germany", "VN": "Vietnam", "KR": "South Korea", "JP": "Japan",
	"GB": "United Kingdom", "FR": "France", "CA": "Canada", "NL": "Netherlands",
	"AU": "Australia", "SE": "Sweden", "CH": "Switzerland", "BR": "Brazil",
	"IN": "India", "PH": "Philippines", "ID": "Indonesia", "MY": "Malaysia",
	"TW": "Taiwan", "FI": "Finland", "NO": "Norway", "DK": "Denmark",
	"PL": "Poland", "CZ": "Czechia", "AT": "Austria", "RU": "Russia",
}

// buildGeoSlices walks reachable SPs, counts unique IPs per country,
// returns the top countries (capped, with "Other" rollup) for the donut +
// table, and the total count of geolocated IPs.
func buildGeoSlices(sps []snapshot.SPRecord) ([]GeoSlice, int) {
	ipsByCC := map[string]map[string]struct{}{}
	for _, sp := range sps {
		if !sp.Reachable {
			continue
		}
		for _, g := range sp.GeoIP {
			cc := strings.ToUpper(strings.TrimSpace(g.CountryCode))
			if cc == "" {
				continue
			}
			if _, ok := ipsByCC[cc]; !ok {
				ipsByCC[cc] = map[string]struct{}{}
			}
			ipsByCC[cc][g.IP] = struct{}{}
		}
	}
	if len(ipsByCC) == 0 {
		return nil, 0
	}
	type cnt struct {
		cc string
		n  int
	}
	var rows []cnt
	total := 0
	for cc, ips := range ipsByCC {
		rows = append(rows, cnt{cc, len(ips)})
		total += len(ips)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].cc < rows[j].cc
	})
	// Take top 7, rollup the rest as "Other"
	topN := 7
	var slices []GeoSlice
	other := 0
	for i, r := range rows {
		if i < topN {
			slices = append(slices, GeoSlice{
				CC:    r.cc,
				Name:  countryNameOf(r.cc),
				Count: r.n,
				Pct:   100 * float64(r.n) / float64(total),
			})
		} else {
			other += r.n
		}
	}
	if other > 0 {
		slices = append(slices, GeoSlice{
			CC:    "Other",
			Name:  "Other",
			Count: other,
			Pct:   100 * float64(other) / float64(total),
		})
	}
	// Assign colors and arc paths
	cumAngle := -math.Pi / 2 // start at top
	for i := range slices {
		c1, c2 := paletteFor(slices[i].CC)
		slices[i].Color = c1
		slices[i].ColorDark = c2
		fraction := slices[i].Pct / 100
		theta := fraction * 2 * math.Pi
		slices[i].ArcPath = arcPath(cumAngle, cumAngle+theta, 95, 60)
		cumAngle += theta
	}
	return slices, total
}

func paletteFor(cc string) (string, string) {
	if c, ok := countryPalette[cc]; ok {
		return c[0], c[1]
	}
	return "#7D8590", "#4A5060"
}
func countryNameOf(cc string) string {
	if n, ok := countryName[cc]; ok {
		return n
	}
	if cc == "Other" {
		return "Other"
	}
	return cc
}

// arcPath returns an SVG d= for a donut slice between angles start..end
// (radians, atan2 convention with y-down) on a circle of radius `r`,
// with an inner cutout of radius `inner`.
func arcPath(start, end, r, inner float64) string {
	x1, y1 := r*math.Cos(start), r*math.Sin(start)
	x2, y2 := r*math.Cos(end), r*math.Sin(end)
	x3, y3 := inner*math.Cos(end), inner*math.Sin(end)
	x4, y4 := inner*math.Cos(start), inner*math.Sin(start)
	largeArc := 0
	if end-start > math.Pi {
		largeArc = 1
	}
	return fmt.Sprintf(
		"M %.2f %.2f A %.2f %.2f 0 %d 1 %.2f %.2f L %.2f %.2f A %.2f %.2f 0 %d 0 %.2f %.2f Z",
		x1, y1, r, r, largeArc, x2, y2,
		x3, y3, inner, inner, largeArc, x4, y4,
	)
}

// geoNarrative returns the "China + HK = 61% of resolved infrastructure IPs" line.
func geoNarrative(slices []GeoSlice) string {
	if len(slices) == 0 {
		return ""
	}
	if len(slices) == 1 {
		return fmt.Sprintf("%s alone accounts for %.0f%% of resolved infrastructure IPs", slices[0].Name, slices[0].Pct)
	}
	top2 := slices[0].Pct + slices[1].Pct
	return fmt.Sprintf("%s + %s = %.0f%% of resolved infrastructure IPs", slices[0].Name, slices[1].Name, top2)
}

// --- OPERATOR TABLE HELPERS ---

// operatorRegions returns the dominant 1-3 country pills for an operator,
// derived from the GeoIP rows of all member SP records (lookup keyed off
// the snapshot via member miner ID is left to the caller; here we accept
// the operator only and reach back via the page data context). Since we
// don't have a back-pointer here, we use the operator's IP list to make
// this self-contained: each unique IP maps to a country only if it shows
// up in our geo cache. Fallback to "—".
//
// We pre-build the lookup once per Render() call.
var operatorIPCC = map[string]string{}

// operatorRegions reads operatorIPCC populated by the runner-side cluster
// pass. This is a template helper; the real geo signal lives in the
// underlying SP records. For simplicity we approximate by scanning the
// operator's IPs against the package-level IP→CC map populated during
// Render().
func operatorRegions(op snapshot.Operator) string {
	seen := map[string]int{}
	for _, ip := range op.IPs {
		cc, ok := operatorIPCC[ip]
		if !ok || cc == "" {
			continue
		}
		seen[cc]++
	}
	if len(seen) == 0 {
		return `—`
	}
	type kv struct {
		cc string
		n  int
	}
	var rows []kv
	for cc, n := range seen {
		rows = append(rows, kv{cc, n})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].n > rows[j].n })
	limit := 3
	if len(rows) < limit {
		limit = len(rows)
	}
	var parts []string
	for i := 0; i < limit; i++ {
		c1, _ := paletteFor(rows[i].cc)
		parts = append(parts, fmt.Sprintf(`<span class="country-pill" style="--cc-color: %s"><span class="country-dot"></span>%s</span>`, c1, rows[i].cc))
	}
	return strings.Join(parts, " ")
}

// operatorMemberSample returns "f01081394, f01081419 +40" style sample
// for the "Sample miner IDs" column.
func operatorMemberSample(op snapshot.Operator, n int) string {
	if len(op.Members) == 0 {
		return ""
	}
	if len(op.Members) <= n {
		return strings.Join(op.Members, ", ")
	}
	return fmt.Sprintf("%s +%d", strings.Join(op.Members[:n], ", "), len(op.Members)-n)
}

// --- FoC FILTER ---

// filterHealthyFoC returns only the FoC providers we want to publicly list:
// HTTPReachable + sane status codes (any 2xx or 3xx). Anything that 4xx/5xx'd
// or didn't connect is hidden, with a count returned for the disclosure.
func filterHealthyFoC(rows []snapshot.FoCNodeRecord) ([]snapshot.FoCNodeRecord, int) {
	var ok []snapshot.FoCNodeRecord
	hidden := 0
	for _, r := range rows {
		if isHealthyFoC(r) {
			ok = append(ok, r)
		} else {
			hidden++
		}
	}
	// Stable order: sort by Name for deterministic rendering
	sort.Slice(ok, func(i, j int) bool { return strings.ToLower(ok[i].Name) < strings.ToLower(ok[j].Name) })
	return ok, hidden
}

func isHealthyFoC(r snapshot.FoCNodeRecord) bool {
	if !r.HTTPReachable {
		return false
	}
	if r.HTTPStatusCode < 200 || r.HTTPStatusCode >= 400 {
		return false
	}
	return true
}

// focRegion returns a country pill for a FoC provider based on resolved GeoIP.
// Falls back to declared location's country code if GeoIP missing.
func focRegion(r snapshot.FoCNodeRecord) string {
	cc := ""
	if len(r.GeoIP) > 0 {
		cc = strings.ToUpper(r.GeoIP[0].CountryCode)
	}
	if cc == "" {
		// Try to pull from declared "C=US;..." prefix
		for _, kv := range strings.Split(r.DeclaredLocation, ";") {
			kv = strings.TrimSpace(kv)
			if strings.HasPrefix(strings.ToUpper(kv), "C=") {
				cc = strings.ToUpper(strings.TrimPrefix(kv, "C="))
				cc = strings.ToUpper(strings.TrimPrefix(cc, "c="))
				break
			}
		}
	}
	if cc == "" {
		return `—`
	}
	c1, _ := paletteFor(cc)
	return fmt.Sprintf(`<span class="country-pill" style="--cc-color: %s"><span class="country-dot"></span>%s</span>`, c1, cc)
}

// populateOperatorIPCC indexes IP→CC across the snapshot so operatorRegions
// can resolve member IPs without each operator carrying its own lookup.
// Called once at the top of Render().
func populateOperatorIPCC(s *snapshot.Snapshot) {
	operatorIPCC = map[string]string{}
	for _, sp := range s.SPs {
		for _, g := range sp.GeoIP {
			if g.IP != "" && g.CountryCode != "" {
				operatorIPCC[g.IP] = g.CountryCode
			}
		}
	}
}
