package cluster

import (
	"reflect"
	"sort"
	"testing"
)

func TestBuild_OwnerUnion(t *testing.T) {
	in := []Identity{
		{MinerID: "f01", Owner: "f3OWNER1", RawBytePower: "100"},
		{MinerID: "f02", Owner: "f3OWNER1", RawBytePower: "200"},
		{MinerID: "f03", Owner: "f3OWNER2", RawBytePower: "50"},
	}
	out := Build(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(out))
	}
	// Largest cluster (300) should come first.
	if out[0].RawBytePower != "300" {
		t.Errorf("first cluster should sum to 300, got %s", out[0].RawBytePower)
	}
	if !reflect.DeepEqual(out[0].Members, []string{"f01", "f02"}) {
		t.Errorf("cluster members: %v", out[0].Members)
	}
}

func TestBuild_TransitiveViaWorker(t *testing.T) {
	// f01 shares Owner with f02
	// f02 shares Worker with f03
	// All three should end up in one cluster.
	in := []Identity{
		{MinerID: "f01", Owner: "OWN", Worker: "W1"},
		{MinerID: "f02", Owner: "OWN", Worker: "W2"},
		{MinerID: "f03", Owner: "OTHER", Worker: "W2"},
	}
	out := Build(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(out))
	}
	sort.Strings(out[0].Members)
	if !reflect.DeepEqual(out[0].Members, []string{"f01", "f02", "f03"}) {
		t.Errorf("members: %v", out[0].Members)
	}
}

func TestBuild_IPCDNCap(t *testing.T) {
	// 60 miners share an IP — exceeds SharedIPCap=50, so they should NOT
	// be unioned by IP. Each stays its own cluster.
	in := make([]Identity, 60)
	for i := range in {
		in[i] = Identity{
			MinerID: "f0" + itoa(i+1),
			Owner:   "owner-" + itoa(i+1), // distinct owner per miner
			IPs:     []string{"1.2.3.4"},  // shared CDN-like IP
		}
	}
	out := Build(in)
	if len(out) != 60 {
		t.Errorf("CDN-shared IP should not union: got %d clusters, want 60", len(out))
	}
}

func TestBuild_IPBelowCap(t *testing.T) {
	// Two miners share an IP, no shared owner/worker. They SHOULD union.
	in := []Identity{
		{MinerID: "f01", Owner: "A", IPs: []string{"1.2.3.4"}},
		{MinerID: "f02", Owner: "B", IPs: []string{"1.2.3.4"}},
	}
	out := Build(in)
	if len(out) != 1 {
		t.Errorf("shared IP under cap should union, got %d clusters", len(out))
	}
}

func TestBuild_PowerAggregation(t *testing.T) {
	in := []Identity{
		{MinerID: "f01", Owner: "OWN", RawBytePower: "1000000000000000", QualityAdjPower: "10000000000000000"},
		{MinerID: "f02", Owner: "OWN", RawBytePower: "2000000000000000", QualityAdjPower: "20000000000000000"},
	}
	out := Build(in)
	if out[0].RawBytePower != "3000000000000000" {
		t.Errorf("raw sum: %s", out[0].RawBytePower)
	}
	if out[0].QualityAdjPower != "30000000000000000" {
		t.Errorf("qa sum: %s", out[0].QualityAdjPower)
	}
}

func TestBuild_NoSignalNoUnion(t *testing.T) {
	in := []Identity{
		{MinerID: "f01"},
		{MinerID: "f02"},
		{MinerID: "f03"},
	}
	out := Build(in)
	if len(out) != 3 {
		t.Errorf("no shared signal should not union; got %d clusters", len(out))
	}
}

func TestBuild_EmptyOwnerNotUnion(t *testing.T) {
	// Empty owner strings on multiple miners should NOT cluster them
	// (they're missing data, not shared identity).
	in := []Identity{
		{MinerID: "f01", Owner: ""},
		{MinerID: "f02", Owner: ""},
		{MinerID: "f03", Owner: "real"},
		{MinerID: "f04", Owner: "real"},
	}
	out := Build(in)
	// Expected: 3 clusters: {f01}, {f02}, {f03,f04}
	if len(out) != 3 {
		t.Errorf("expected 3 clusters with mixed empty/real owners, got %d", len(out))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
