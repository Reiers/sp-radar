package upgrade

import (
	"testing"
	"time"

	"github.com/Reiers/sp-radar/internal/detect"
	"github.com/Reiers/sp-radar/internal/snapshot"
)

func TestMeetsMinimum(t *testing.T) {
	cases := []struct {
		have string
		min  string
		want bool
	}{
		// Same version
		{"1.36.0", "1.36.0", true},
		{"v1.36.0", "1.36.0", true},

		// Above
		{"1.36.1", "1.36.0", true},
		{"1.37.0", "1.36.0", true},
		{"2.0.0", "1.36.0", true},

		// Below
		{"1.35.1", "1.36.0", false},
		{"1.34.4", "1.36.0", false},
		{"0.30.4", "0.32.4", false},

		// Pre-release: an rc of the target release counts as ready
		{"1.36.0-rc1", "1.36.0", true},
		{"1.36.0-rc5", "1.36.0", true},

		// Pre-release of below-target: not ready
		{"1.35.1-rc1", "1.36.0", false},

		// Weird real-world suffixes from filcensus snapshots (verified 2026-05-12)
		{"1.34.1_sdminer_v1", "1.36.0", false},
		{"1.34.1.25091910", "1.36.0", false},
		{"1.36.0_sdminer_v1", "1.36.0", true},
		{"0.32.4 \"Mild Inconvenience\"", "0.32.4", true},

		// Empty / garbage
		{"", "1.36.0", false},
		{"garbage", "1.36.0", false},
	}
	for _, c := range cases {
		got := meetsMinimum(c.have, c.min)
		if got != c.want {
			t.Errorf("meetsMinimum(%q, %q) = %v, want %v", c.have, c.min, got, c.want)
		}
	}
}

func TestClassify_SyntheticFleet(t *testing.T) {
	// Build a small synthetic snapshot resembling the live mainnet distribution
	// we sampled on 2026-05-12: mostly Lotus 1.34.x, a sliver on 1.36.0-rc1,
	// a handful of Forest + Venus, and some unknowns.
	s := &snapshot.Snapshot{
		GeneratedAt: time.Date(2026, 5, 12, 12, 39, 4, 0, time.UTC),
		ChainNodes: []snapshot.ChainNodeRecord{
			// 4 ready Lotus (1.36.0-rc1)
			{Software: string(detect.Lotus), SoftwareVersion: "1.36.0-rc1"},
			{Software: string(detect.Lotus), SoftwareVersion: "1.36.0-rc1"},
			{Software: string(detect.Lotus), SoftwareVersion: "1.36.0-rc1"},
			{Software: string(detect.Lotus), SoftwareVersion: "1.36.0-rc1"},
			// 3 not-ready Lotus
			{Software: string(detect.Lotus), SoftwareVersion: "1.34.1"},
			{Software: string(detect.Lotus), SoftwareVersion: "1.34.0"},
			{Software: string(detect.Lotus), SoftwareVersion: "1.35.1"},
			// 1 lotus unknown version
			{Software: string(detect.Lotus), SoftwareVersion: ""},
			// Forest: 0.33.4 = ready (mainnet release). 0.33.3 = not ready
			// (calibnet-only). 0.30.4 = not ready.
			{Software: string(detect.Forest), SoftwareVersion: "0.33.4"},
			{Software: string(detect.Forest), SoftwareVersion: "0.33.3"},
			{Software: string(detect.Forest), SoftwareVersion: "0.30.4"},
			// 1 venus unknown version, 1 not-ready
			{Software: string(detect.Venus), SoftwareVersion: ""},
			{Software: string(detect.Venus), SoftwareVersion: "1.18.0"},
			// 2 boost nodes — shouldn't count toward nv readiness
			{Software: string(detect.Boost), SoftwareVersion: "2.4.4"},
			{Software: string(detect.Boost), SoftwareVersion: "2.4.4"},
		},
	}

	r := Classify(s, NV28FireHorse)

	// Aggregate. Venus is in a Pending bucket so its 2 peers do NOT count
	// toward TotalPeers/Ready/NotReady/Unknown.
	if r.TotalPeers != 11 { // 8 lotus + 3 forest = 11 gating peers
		t.Errorf("TotalPeers: got %d, want 11", r.TotalPeers)
	}
	if r.Ready != 5 { // 4 lotus-rc1 + 1 forest-0.33.4 = 5
		t.Errorf("Ready: got %d, want 5", r.Ready)
	}
	if r.NotReady != 5 { // 3 lotus-old + 2 forest-old
		t.Errorf("NotReady: got %d, want 5", r.NotReady)
	}
	if r.Unknown != 1 { // 1 lotus unknown (venus unknown goes to Pending bucket, not counted)
		t.Errorf("Unknown: got %d, want 1", r.Unknown)
	}
	if r.OtherPeers != 2 { // 2 boost
		t.Errorf("OtherPeers: got %d, want 2", r.OtherPeers)
	}

	// Lotus bucket
	var lotus *Bucket
	for i, b := range r.Buckets {
		if b.Software == string(detect.Lotus) {
			lotus = &r.Buckets[i]
			break
		}
	}
	if lotus == nil {
		t.Fatal("Lotus bucket missing")
	}
	if lotus.Total != 8 || lotus.Ready != 4 || lotus.NotReady != 3 || lotus.Unknown != 1 {
		t.Errorf("Lotus bucket: got total=%d ready=%d notReady=%d unknown=%d, want 8/4/3/1",
			lotus.Total, lotus.Ready, lotus.NotReady, lotus.Unknown)
	}

	// Top versions ordering: rc1 should be #1 (4 nodes), then 1.34.1 etc.
	if len(lotus.TopVersions) < 1 || lotus.TopVersions[0].Version != "1.36.0-rc1" {
		t.Errorf("TopVersions[0]: got %+v, want 1.36.0-rc1", lotus.TopVersions[0])
	}
	if !lotus.TopVersions[0].Ready {
		t.Error("1.36.0-rc1 should be flagged Ready=true")
	}

	// Venus bucket must be Pending.
	var venus *Bucket
	for i, b := range r.Buckets {
		if b.Software == string(detect.Venus) {
			venus = &r.Buckets[i]
			break
		}
	}
	if venus == nil {
		t.Fatal("Venus bucket missing")
	}
	if !venus.Pending {
		t.Error("Venus bucket should be Pending (no mainnet nv28 release)")
	}
	if venus.Total != 2 {
		t.Errorf("Venus Total: got %d, want 2 (observed peers should still be counted)", venus.Total)
	}
	if venus.Ready != 0 || venus.NotReady != 0 || venus.Unknown != 0 {
		t.Errorf("Venus Pending should leave Ready/NotReady/Unknown at 0; got %d/%d/%d",
			venus.Ready, venus.NotReady, venus.Unknown)
	}
}
