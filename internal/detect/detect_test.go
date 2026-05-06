package detect

import "testing"

func TestClassify_Agents(t *testing.T) {
	cases := []struct {
		name     string
		agent    string
		protos   []string
		wantSW   Software
		wantVer  string
		wantRole Role
	}{
		// --- chain nodes ---
		{
			name:     "lotus full node typical",
			agent:    "lotus-1.34.1+mainnet+git_abcdef",
			wantSW:   Lotus,
			wantVer:  "1.34.1",
			wantRole: RoleChainNode,
		},
		{
			name:     "lotus full node bare",
			agent:    "lotus",
			wantSW:   Lotus,
			wantVer:  "",
			wantRole: RoleChainNode,
		},
		{
			name:     "lotus full node v1.35.0 release",
			agent:    "lotus-1.35.0",
			wantSW:   Lotus,
			wantVer:  "1.35.0",
			wantRole: RoleChainNode,
		},
		{
			name:     "forest with git suffix",
			agent:    "forest-0.30.0+git.abc1234",
			wantSW:   Forest,
			wantVer:  "0.30.0",
			wantRole: RoleChainNode,
		},
		{
			name:     "forest release",
			agent:    "forest-0.31.1",
			wantSW:   Forest,
			wantVer:  "0.31.1",
			wantRole: RoleChainNode,
		},
		{
			name:     "venus bare (libp2p.UserAgent(\"venus\"))",
			agent:    "venus",
			wantSW:   Venus,
			wantVer:  "",
			wantRole: RoleChainNode,
		},
		{
			name:     "venus with version suffix",
			agent:    "venus-1.16.0",
			wantSW:   Venus,
			wantVer:  "1.16.0",
			wantRole: RoleChainNode,
		},

		// --- SPs ---
		{
			name:     "curio versioned",
			agent:    "curio-1.24.4+mainnet",
			wantSW:   Curio,
			wantVer:  "1.24.4",
			wantRole: RoleSP,
		},
		{
			name:     "boost slash style",
			agent:    "boost/1.7.6",
			wantSW:   Boost,
			wantVer:  "1.7.6",
			wantRole: RoleSP,
		},
		{
			name:     "boost dash style with rc",
			agent:    "boost-1.7.6-rc1",
			wantSW:   Boost,
			wantVer:  "1.7.6-rc1",
			wantRole: RoleSP,
		},
		{
			name:     "lotus-miner explicit",
			agent:    "lotus-miner-1.34.1",
			wantSW:   LotusMiner,
			wantVer:  "1.34.1",
			wantRole: RoleSP,
		},
		{
			name:     "venus-miner",
			agent:    "venus-miner-1.16.0",
			wantSW:   VenusMiner,
			wantVer:  "1.16.0",
			wantRole: RoleSP,
		},
		{
			name:     "droplet (ex-market)",
			agent:    "droplet-1.16.0",
			wantSW:   Droplet,
			wantVer:  "1.16.0",
			wantRole: RoleSP,
		},

		// --- empty / unknown ---
		{
			name:     "empty agent",
			agent:    "",
			wantSW:   Unknown,
			wantVer:  "",
			wantRole: RoleUnknown,
		},
		{
			name:     "garbage",
			agent:    "totally-not-a-filecoin-node/0.0.0",
			wantSW:   Unknown,
			wantVer:  "",
			wantRole: RoleUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Classify(tc.agent, tc.protos)
			if r.Software != tc.wantSW {
				t.Errorf("Software: got %q, want %q", r.Software, tc.wantSW)
			}
			if r.Version != tc.wantVer {
				t.Errorf("Version: got %q, want %q", r.Version, tc.wantVer)
			}
			if r.Role != tc.wantRole {
				t.Errorf("Role: got %q, want %q", r.Role, tc.wantRole)
			}
		})
	}
}

func TestClassify_ProtocolFallback(t *testing.T) {
	// Empty/unknown agent but markets protocols → should still classify as SP
	cases := []struct {
		name     string
		protos   []string
		wantSW   Software
		wantRole Role
	}{
		{
			name:     "boost markets protocol no agent",
			protos:   []string{"/fil/storage/mk/1.2.0", "/ipfs/id/1.0.0"},
			wantSW:   Boost,
			wantRole: RoleSP,
		},
		{
			name:     "legacy markets protocol no agent",
			protos:   []string{"/fil/storage/mk/1.1.0"},
			wantSW:   Markets,
			wantRole: RoleSP,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Classify("", tc.protos)
			if r.Software != tc.wantSW {
				t.Errorf("Software: got %q, want %q", r.Software, tc.wantSW)
			}
			if r.Role != tc.wantRole {
				t.Errorf("Role: got %q, want %q", r.Role, tc.wantRole)
			}
		})
	}
}

func TestClassify_IndexerCapability(t *testing.T) {
	cases := []struct {
		name   string
		protos []string
		want   bool
	}{
		{"legs head", []string{"/legs/head/0.0.1"}, true},
		{"indexer ingest", []string{"/indexer/ingest/2.0.0"}, true},
		{"none", []string{"/ipfs/id/1.0.0"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Classify("lotus-1.34.0", tc.protos)
			if r.IndexerCapable != tc.want {
				t.Errorf("IndexerCapable: got %v, want %v", r.IndexerCapable, tc.want)
			}
		})
	}
}

func TestClassifyFromSPContext_LotusBecomesMiner(t *testing.T) {
	// A bare "lotus-X" agent observed at a chain-listed SP must be lotus-miner,
	// not a full chain node.
	r := ClassifyFromSPContext("lotus-1.34.1", nil)
	if r.Software != LotusMiner {
		t.Errorf("expected LotusMiner from SP context, got %q", r.Software)
	}
	if r.Role != RoleSP {
		t.Errorf("expected RoleSP, got %q", r.Role)
	}
	if r.Version != "1.34.1" {
		t.Errorf("expected version 1.34.1, got %q", r.Version)
	}
}

func TestClassifyFromSPContext_PreservesOthers(t *testing.T) {
	// Curio context shouldn't be touched
	r := ClassifyFromSPContext("curio-1.24.4", nil)
	if r.Software != Curio {
		t.Errorf("expected Curio, got %q", r.Software)
	}
	if r.Role != RoleSP {
		t.Errorf("expected RoleSP, got %q", r.Role)
	}
}

func TestExtractVersion(t *testing.T) {
	cases := map[string]string{
		"lotus-1.34.1+mainnet+git_abc": "1.34.1",
		"forest-0.30.0+git.abc":        "0.30.0",
		"boost/1.7.6":                  "1.7.6",
		"boost-1.7.6-rc1":              "1.7.6-rc1",
		"curio-1.24.4":                 "1.24.4",
		"":                             "",
		"lotus":                        "",
		"venus":                        "",
	}
	for in, want := range cases {
		if got := extractVersion(in); got != want {
			t.Errorf("extractVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
