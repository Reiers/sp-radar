package render

import (
	"fmt"
	"math"
	"strings"

	"github.com/Reiers/sp-radar/internal/snapshot"
)

// HistPoint is one quarterly observation of network raw / QA power.
type HistPoint struct {
	Label string  // "Oct 20", "Jan 21", ...
	Raw   float64 // EiB
	QA    float64 // EiB
}

// powerHistory is the canonical quarterly raw/QA-power timeseries we use to
// draw the Network Power History chart. The pre-2026 readings are taken from
// the SP Census report (filecoin-sp-census-public.html) and reflect chain
// state at quarter boundaries. The most recent point is appended live from
// the snapshot's NetworkTruth so the chart always ends on "today".
//
// Values in EiB. The story this draws: raw power peaked at 16.77 EiB in
// Jul 2022; QAP peaked later (Dec 2023) at ~25 EiB because the 10× verified-
// deal multiplier delayed the visible decline. Today raw is ~1.9 EiB while
// QAP is ~16 EiB — an 8.7× ratio that hides the physical exodus.
var powerHistory = []HistPoint{
	{"Oct 20", 0.80, 0.80},
	{"Jan 21", 2.00, 2.00},
	{"Apr 21", 4.50, 4.50},
	{"Jul 21", 8.50, 8.50},
	{"Oct 21", 13.50, 13.50},
	{"Jan 22", 15.50, 16.00},
	{"Apr 22", 16.50, 18.50},
	{"Jul 22", 16.77, 19.00},
	{"Oct 22", 15.80, 18.50},
	{"Jan 23", 13.50, 19.00},
	{"Apr 23", 11.80, 20.00},
	{"Jul 23", 10.20, 22.00},
	{"Oct 23", 8.50, 24.50},
	{"Jan 24", 7.00, 25.00},
	{"Apr 24", 5.80, 23.00},
	{"Jul 24", 4.80, 21.00},
	{"Oct 24", 4.00, 19.50},
	{"Jan 25", 3.30, 18.00},
	{"Apr 25", 2.80, 17.50},
	{"Jul 25", 2.40, 17.00},
	{"Oct 25", 2.20, 16.50},
	{"Jan 26", 2.00, 16.30},
}

// PowerHistoryWithLatest returns the canonical history with today's snapshot
// appended (label "today"). If NetworkTruth is missing/zero, returns the
// canonical series unchanged.
func PowerHistoryWithLatest(s *snapshot.Snapshot) []HistPoint {
	out := append([]HistPoint(nil), powerHistory...)
	if s == nil || s.NetworkTruth == nil {
		return out
	}
	rawEiB := s.NetworkTruth.RawPiB / 1024
	qaEiB := s.NetworkTruth.QAPiB / 1024
	if rawEiB <= 0 && qaEiB <= 0 {
		return out
	}
	out = append(out, HistPoint{
		Label: s.GeneratedAt.Format("Jan 06"),
		Raw:   rawEiB,
		QA:    qaEiB,
	})
	return out
}

// PowerHistorySVG returns the inner content of the history chart SVG:
// gridlines + axis labels + raw line + QA line + legend. Designed to be
// dropped inside a <svg viewBox="0 0 1080 360"> wrapper. Pure markup,
// no JS, no external deps.
//
// Plot frame: x in [60, 1060] (60px left padding for Y labels, 20px right),
// y in [20, 300] (top padding 20, bottom padding 60 for X labels).
// Y axis = 0..maxEiB scaled to plot height; X axis = even spacing across N points.
func PowerHistorySVG(points []HistPoint) string {
	if len(points) < 2 {
		return ""
	}
	// Find Y max with some headroom.
	maxY := 0.0
	for _, p := range points {
		if p.Raw > maxY {
			maxY = p.Raw
		}
		if p.QA > maxY {
			maxY = p.QA
		}
	}
	// Round up to a clean number for axis labelling.
	maxAxis := math.Ceil(maxY/5) * 5
	if maxAxis < 5 {
		maxAxis = 5
	}

	plotX0, plotX1 := 60.0, 1060.0
	plotY0, plotY1 := 20.0, 300.0 // top, bottom

	xFor := func(i int) float64 {
		if len(points) == 1 {
			return (plotX0 + plotX1) / 2
		}
		return plotX0 + float64(i)*(plotX1-plotX0)/float64(len(points)-1)
	}
	yFor := func(v float64) float64 {
		clamped := math.Max(0, math.Min(maxAxis, v))
		return plotY1 - (clamped/maxAxis)*(plotY1-plotY0)
	}

	var sb strings.Builder

	// Y gridlines + labels at 0, 25%, 50%, 75%, 100% of axis
	sb.WriteString(`<g class="grid">`)
	for _, frac := range []float64{0, 0.25, 0.5, 0.75, 1.0} {
		y := plotY1 - frac*(plotY1-plotY0)
		sb.WriteString(fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`, plotX0, y, plotX1, y))
	}
	sb.WriteString(`</g>`)

	// Y axis labels (left side)
	for _, frac := range []float64{0, 0.25, 0.5, 0.75, 1.0} {
		y := plotY1 - frac*(plotY1-plotY0)
		val := frac * maxAxis
		sb.WriteString(fmt.Sprintf(`<text x="50" y="%.1f" text-anchor="end" class="axis-label">%.0f EiB</text>`, y+4, val))
	}

	// X axis labels — only every 4th to avoid overlap
	step := 1
	if len(points) > 12 {
		step = (len(points) + 9) / 10 // aim for ~10 labels
	}
	for i, p := range points {
		if i%step != 0 && i != len(points)-1 {
			continue
		}
		x := xFor(i)
		sb.WriteString(fmt.Sprintf(`<text x="%.1f" y="328" text-anchor="middle" class="axis-label">%s</text>`, x, p.Label))
	}

	// Annotations: peak markers
	rawPeakIdx, qaPeakIdx := 0, 0
	for i, p := range points {
		if p.Raw > points[rawPeakIdx].Raw {
			rawPeakIdx = i
		}
		if p.QA > points[qaPeakIdx].QA {
			qaPeakIdx = i
		}
	}
	// Raw peak callout
	rpx, rpy := xFor(rawPeakIdx), yFor(points[rawPeakIdx].Raw)
	sb.WriteString(fmt.Sprintf(
		`<g class="peak-marker"><circle cx="%.1f" cy="%.1f" r="5" fill="#1565C0" stroke="#fff" stroke-width="2"/><text x="%.1f" y="%.1f" text-anchor="middle" class="peak-label">peak %.2f EiB · %s</text></g>`,
		rpx, rpy, rpx, rpy-12, points[rawPeakIdx].Raw, points[rawPeakIdx].Label))
	// QAP peak callout (only if different from raw peak point)
	if qaPeakIdx != rawPeakIdx {
		qpx, qpy := xFor(qaPeakIdx), yFor(points[qaPeakIdx].QA)
		sb.WriteString(fmt.Sprintf(
			`<g class="peak-marker"><circle cx="%.1f" cy="%.1f" r="5" fill="#6A1B9A" stroke="#fff" stroke-width="2"/><text x="%.1f" y="%.1f" text-anchor="middle" class="peak-label">QAP peak %.1f EiB · %s</text></g>`,
			qpx, qpy, qpx, qpy-12, points[qaPeakIdx].QA, points[qaPeakIdx].Label))
	}

	// Build the path data for both series
	rawD := strings.Builder{}
	qaD := strings.Builder{}
	for i, p := range points {
		x := xFor(i)
		yR := yFor(p.Raw)
		yQ := yFor(p.QA)
		if i == 0 {
			rawD.WriteString(fmt.Sprintf("M %.1f %.1f", x, yR))
			qaD.WriteString(fmt.Sprintf("M %.1f %.1f", x, yQ))
		} else {
			rawD.WriteString(fmt.Sprintf(" L %.1f %.1f", x, yR))
			qaD.WriteString(fmt.Sprintf(" L %.1f %.1f", x, yQ))
		}
	}

	// QAP (dashed purple) — drawn first so raw line sits on top
	sb.WriteString(fmt.Sprintf(`<path class="hist-qa" d="%s"/>`, qaD.String()))
	// Raw (solid blue) with subtle fill
	rawFill := rawD.String() + fmt.Sprintf(" L %.1f %.1f L %.1f %.1f Z", plotX1, plotY1, plotX0, plotY1)
	sb.WriteString(fmt.Sprintf(`<path class="hist-raw-fill" d="%s"/>`, rawFill))
	sb.WriteString(fmt.Sprintf(`<path class="hist-raw" d="%s"/>`, rawD.String()))

	// Legend
	sb.WriteString(`<g class="hist-legend" transform="translate(80 12)">`)
	sb.WriteString(`<rect x="0" y="-4" width="14" height="3" fill="#1565C0"/>`)
	sb.WriteString(`<text x="20" y="0" class="legend-label">Raw power (EiB)</text>`)
	sb.WriteString(`<rect x="160" y="-4" width="14" height="3" fill="#6A1B9A"/>`)
	sb.WriteString(`<text x="180" y="0" class="legend-label">Quality-adjusted (EiB)</text>`)
	sb.WriteString(`</g>`)

	return sb.String()
}

// --- Network sentiment meter (CMC-style fear/greed dial) ---

// SentimentMeter holds the data used to render the half-circle gauge.
type SentimentMeter struct {
	// Score is 0..100. 0 = collapsing (network shrinking fast), 100 = booming.
	// Mapped from the percentage change of network raw power between the
	// previous snapshot and now (capped to [-10%, +10%] for sensitivity).
	Score float64
	// Label is the human-readable sentiment ("Decline", "Stable", "Growth", etc.)
	Label string
	// AccentColor is the dial colour (red → green spectrum).
	AccentColor string
	// Delta is the underlying raw-power change in PiB over the measurement
	// period (can be negative); shown under the gauge.
	DeltaPiB float64
	// PriorRawPiB is the raw power we compared against, in PiB.
	PriorRawPiB float64
}

// BuildSentimentMeter returns a SentimentMeter for snapshot s.
//
// PREFERRED path: when a prior snapshot is available, the meter is a TRUE
// snapshot-over-snapshot diff of whole-network raw power (s.raw - prior.raw).
// This is the honest "is the network growing or shrinking since last time"
// signal and it reflects the absolute network trend across our own snapshot
// history.
//
// FALLBACK path (prior == nil, i.e. first-ever snapshot): sum the per-miner
// Filfox measurement-period deltas. This is noisier and only measures Filfox's
// own intra-period window, but it's the best we can do with no history.
//
// Score buckets (mapped from delta-as-pct of raw):
//
//	delta <= -2%   -> 0..15  ("Collapsing")
//	delta in -2..-0.5%  -> 15..40  ("Declining")
//	delta in -0.5..+0.5% -> 40..60 ("Stable")
//	delta in +0.5..+2%   -> 60..85 ("Growing")
//	delta >= +2%   -> 85..100 ("Booming")
func BuildSentimentMeter(s *snapshot.Snapshot) SentimentMeter {
	return BuildSentimentMeterVs(s, nil)
}

// BuildSentimentMeterVs computes the meter for s, diffing against prior when
// prior is non-nil (true snapshot-over-snapshot trend).
func BuildSentimentMeterVs(s, prior *snapshot.Snapshot) SentimentMeter {
	m := SentimentMeter{Score: 50, Label: "Stable", AccentColor: "#8493AE"}
	if s == nil || s.NetworkTruth == nil {
		return m
	}
	const piB = float64(1 << 50)

	var netDelta, priorRaw float64
	curRaw := s.NetworkTruth.RawPiB * piB

	if prior != nil && prior.NetworkTruth != nil && prior.NetworkTruth.RawPiB > 0 {
		// True trend: difference in absolute network raw power between the
		// previous snapshot and this one.
		priorRaw = prior.NetworkTruth.RawPiB * piB
		netDelta = curRaw - priorRaw
	} else {
		// No history yet: fall back to summed Filfox per-miner deltas.
		if len(s.SPs) == 0 {
			return m
		}
		for _, sp := range s.SPs {
			if sp.RawBytePowerDelta == "" || sp.RawBytePowerDelta == "0" {
				continue
			}
			netDelta += parseBigToFloat(sp.RawBytePowerDelta)
		}
		priorRaw = curRaw - netDelta
	}

	pctChange := 0.0
	if priorRaw > 0 {
		pctChange = (netDelta / priorRaw) * 100
	}

	// Normalise the bucket score to a ~monthly rate so the dial reflects the
	// PACE of change, not the (variable) gap between snapshots. A -6.7% drop
	// over 24 days and the same drop over 2 days are very different signals;
	// the meter buckets (±2% = extremes) are calibrated for a ~30-day window.
	// The DISPLAYED DeltaPiB / PriorRawPiB below stay as the real absolute
	// change between the two snapshots; only the score mapping is rate-scaled.
	scorePct := pctChange
	if prior != nil && prior.NetworkTruth != nil {
		if days := s.GeneratedAt.Sub(prior.GeneratedAt).Hours() / 24; days > 0.5 {
			scorePct = pctChange * (30.0 / days)
		}
	}

	// Map scorePct into [0,100]: clamp to [-2, +2] then scale linearly
	clamp := scorePct
	if clamp < -2 {
		clamp = -2
	}
	if clamp > 2 {
		clamp = 2
	}
	score := (clamp + 2) / 4 * 100 // -2 -> 0, 0 -> 50, +2 -> 100

	switch {
	case score <= 15:
		m.Label = "Collapsing"
		m.AccentColor = "#D32F2F"
	case score <= 40:
		m.Label = "Declining"
		m.AccentColor = "#D2691E"
	case score <= 60:
		m.Label = "Stable"
		m.AccentColor = "#B8860B"
	case score <= 85:
		m.Label = "Growing"
		m.AccentColor = "#2E8B57"
	default:
		m.Label = "Booming"
		m.AccentColor = "#1B5E20"
	}
	m.Score = score
	m.DeltaPiB = netDelta / piB
	m.PriorRawPiB = priorRaw / piB
	return m
}

func parseBigToFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// math.Big avoidance: read sign, parse as float64 (works for the deltas
	// we see, which fit in 2^53 easily — biggest is ~1.5 PiB ~ 1.7e15)
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	}
	v := 0.0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		v = v*10 + float64(c-'0')
	}
	if neg {
		v = -v
	}
	return v
}

// SentimentMeterArc returns the SVG path for the *filled* portion of the
// gauge (the gradient arc from the left edge up to the score position).
// Score is 0..100. The background full arc is rendered separately by the
// template.
//
// Geometry: half-circle on a 200×144 viewbox.
//
//   cx, cy = (100, 110), r = 85
//   left  edge of arc:   (15,  110)  — angle = π
//   top   of arc:        (100, 25)   — angle = 3π/2 (= -π/2)
//   right edge of arc:   (185, 110)  — angle = 2π   (= 0)
//
// SVG y grows downward, so to land on the upper semicircle the angle
// must produce a negative sin(angle). We sweep from π → 2π (clockwise
// visually, A-arc sweep-flag=1).
func sentimentAngle(score float64) float64 {
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return math.Pi + (score/100)*math.Pi
}

func SentimentMeterArc(score float64) string {
	cx, cy, r := 100.0, 110.0, 85.0
	start := math.Pi
	end := sentimentAngle(score)
	x1 := cx + r*math.Cos(start)
	y1 := cy + r*math.Sin(start)
	x2 := cx + r*math.Cos(end)
	y2 := cy + r*math.Sin(end)
	// large-arc-flag: 0 because span is always ≤ 180° (we go from π to at most 2π).
	// sweep-flag = 1 (positive-angle direction, which is the upper half here).
	return fmt.Sprintf("M %.2f %.2f A %.2f %.2f 0 0 1 %.2f %.2f", x1, y1, r, r, x2, y2)
}

// SentimentMeterTickX / Y position the indicator dot on the arc at the
// given score (0..100). Same geometry as the arc.
func SentimentMeterTickX(score float64) float64 {
	return 100 + 85*math.Cos(sentimentAngle(score))
}
func SentimentMeterTickY(score float64) float64 {
	return 110 + 85*math.Sin(sentimentAngle(score))
}
