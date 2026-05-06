// Package snapshot defines the on-disk JSON format filcensus writes once per
// run. Every collector phase populates a section of Snapshot; the renderer
// consumes Snapshot to produce the static dashboard.
//
// Stability: this is an append-only schema. Existing fields must never change
// type or semantics; new fields are added with zero/empty defaults so older
// snapshots remain readable. Bump SchemaVersion on breaking changes.
package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// SchemaVersion is bumped on incompatible schema changes.
const SchemaVersion = 1

// Snapshot is the top-level snapshot document. One file per run.
type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	Network       string    `json:"network"` // "mainnet" or "calibration"
	GeneratedAt   time.Time `json:"generated_at"`
	GeneratedBy   string    `json:"generated_by"` // host + tool version

	// On-chain context the run was based on
	ChainHead ChainHead `json:"chain_head"`

	// Per-phase results
	SPs        []SPRecord        `json:"sps"`
	ChainNodes []ChainNodeRecord `json:"chain_nodes"`
	FoCNodes   []FoCNodeRecord   `json:"foc_nodes"`

	// Operators is the deduplicated SP fleet — miner IDs grouped by shared
	// owner / worker / control / beneficiary / IP. This is the real shape of
	// the network. Computed by internal/cluster after the SP probe completes.
	Operators []Operator `json:"operators,omitempty"`

	// Aggregated counts (computed by the runner before write, for convenience)
	Aggregates Aggregates `json:"aggregates"`

	// Run-level diagnostics
	Run RunStats `json:"run"`
}

// ChainHead pins the snapshot to a specific chain state.
type ChainHead struct {
	Height int64    `json:"height"`
	TipSet []string `json:"tipset_cids,omitempty"`
}

// SPRecord is one row per chain-listed storage provider that we observed.
// Fields are populated best-effort; callers should treat empty values as
// "not collected" rather than "absent on chain".
type SPRecord struct {
	MinerID string `json:"miner_id"` // f0xxxx address

	// Chain-derived fields
	PeerID            string   `json:"peer_id,omitempty"`
	Multiaddrs        []string `json:"multiaddrs,omitempty"`
	SectorSize        int64    `json:"sector_size_bytes,omitempty"`
	RawBytePower      string   `json:"raw_byte_power,omitempty"`     // big-int string
	QualityAdjPower   string   `json:"quality_adj_power,omitempty"`  // big-int string
	HasMinPower       bool     `json:"has_min_power"`
	OwnerAddr         string   `json:"owner_addr,omitempty"`
	WorkerAddr        string   `json:"worker_addr,omitempty"`
	BeneficiaryAddr   string   `json:"beneficiary_addr,omitempty"`
	ControlAddrs      []string `json:"control_addrs,omitempty"`

	// libp2p probe fields
	Reachable        bool          `json:"reachable"`
	DialError        string        `json:"dial_error,omitempty"`
	DialDuration     time.Duration `json:"dial_duration_ns,omitempty"`
	AgentVersion     string        `json:"agent_version,omitempty"`
	Protocols        []string      `json:"protocols,omitempty"`
	Software         string        `json:"software,omitempty"`         // detect.Software
	SoftwareVersion  string        `json:"software_version,omitempty"`
	IndexerCapable   bool          `json:"indexer_capable"`

	// Network-resolved fields
	IPs       []string `json:"ips,omitempty"`
	GeoIP     []GeoRow `json:"geoip,omitempty"`

	// External signals from third-party sources (Filfox, Filrep, ...).
	// SourceTags is the set of sources that reported this SP as active.
	SourceTags []string `json:"source_tags,omitempty"`

	// FilrepReachability and FilrepUptime are passthrough metadata from Filrep
	// (api.filrep.io). They reflect Filrep's measurements over time, which is
	// orthogonal to our own one-shot libp2p probe.
	FilrepReachability string  `json:"filrep_reachability,omitempty"` // "reachable" | "unreachable" | "unknown"
	FilrepUptime       float64 `json:"filrep_uptime,omitempty"`
	FilrepCountryCode  string  `json:"filrep_country_code,omitempty"`
}

// ChainNodeRecord is one row per chain (full-node) peer we discovered via
// our own NetPeers + DHT crawl.
type ChainNodeRecord struct {
	PeerID          string   `json:"peer_id"`
	Multiaddrs      []string `json:"multiaddrs,omitempty"`
	AgentVersion    string   `json:"agent_version,omitempty"`
	Protocols       []string `json:"protocols,omitempty"`
	Software        string   `json:"software,omitempty"`
	SoftwareVersion string   `json:"software_version,omitempty"`
	Reachable       bool     `json:"reachable"`
	IPs             []string `json:"ips,omitempty"`
	GeoIP           []GeoRow `json:"geoip,omitempty"`

	// Optional: best-effort role hints (e.g. "bootstrap", "archival").
	// Initially empty; we may populate later.
	RoleHints []string `json:"role_hints,omitempty"`
}

// FoCNodeRecord is one row per provider in the on-chain ServiceProviderRegistry.
//
// Source contracts (mainnet / calibration):
//   ServiceProviderRegistry  0xf55dDbf63F1b55c3F1D4FA7e339a68AB7b64A5eB / 0x839e5c9988e4e9977d40708d0094103c0839Ac9D
type FoCNodeRecord struct {
	ProviderID         string `json:"provider_id"` // bigint as decimal string
	ServiceProviderHex string `json:"service_provider"` // 0x... operator address
	PayeeHex           string `json:"payee"`            // 0x... payment recipient
	Name               string `json:"name"`
	Description        string `json:"description,omitempty"`
	Active             bool   `json:"active"`
	ProductType        string `json:"product_type"` // "PDP" today

	// PDP offering (decoded from on-chain capability k/v map)
	ServiceURL                string `json:"service_url,omitempty"`
	DeclaredLocation          string `json:"declared_location,omitempty"`
	IPNIPeerID                string `json:"ipni_peer_id,omitempty"`
	IPNISupportsPiece         bool   `json:"ipni_supports_piece"`
	IPNISupportsIPFS          bool   `json:"ipni_supports_ipfs"`
	MinPieceSizeBytes         string `json:"min_piece_size_bytes,omitempty"`
	MaxPieceSizeBytes         string `json:"max_piece_size_bytes,omitempty"`
	StoragePricePerTibPerDay  string `json:"storage_price_per_tib_per_day,omitempty"`
	MinProvingPeriodEpochs    string `json:"min_proving_period_epochs,omitempty"`
	PaymentTokenAddress       string `json:"payment_token_address,omitempty"`

	// HTTP probe of serviceURL/pdp/ping
	HTTPReachable    bool   `json:"http_reachable"`
	HTTPStatusCode   int    `json:"http_status_code,omitempty"`
	HTTPServerHeader string `json:"http_server_header,omitempty"`
	HTTPError        string `json:"http_error,omitempty"`

	// libp2p identify on IPNIPeerID
	LibP2PReachable       bool     `json:"libp2p_reachable"`
	LibP2PAgentVersion    string   `json:"libp2p_agent_version,omitempty"`
	LibP2PProtocols       []string `json:"libp2p_protocols,omitempty"`
	LibP2PSoftware        string   `json:"libp2p_software,omitempty"`
	LibP2PSoftwareVersion string   `json:"libp2p_software_version,omitempty"`

	// Network-resolved fields from serviceURL hostname
	ResolvedIPs   []string `json:"resolved_ips,omitempty"`
	GeoIP         []GeoRow `json:"geoip,omitempty"`
	LocationMatch string   `json:"location_match,omitempty"` // "match" | "mismatch" | "unknown"
}

// Operator is one deduplicated entity that controls one or more miner IDs.
type Operator struct {
	Representative  string   `json:"representative"` // canonical (lowest) miner ID
	Members         []string `json:"members"`        // sorted miner IDs in this operator
	Owners          []string `json:"owners,omitempty"`
	Workers         []string `json:"workers,omitempty"`
	Beneficiaries   []string `json:"beneficiaries,omitempty"`
	IPs             []string `json:"ips,omitempty"`
	RawBytePower    string   `json:"raw_byte_power"`
	QualityAdjPower string   `json:"quality_adj_power"`

	// Reachability rollup (computed during cluster aggregation):
	// how many of this operator's miner IDs we could libp2p-dial.
	ReachableMembers   int `json:"reachable_members"`
	UnreachableMembers int `json:"unreachable_members"`
}

// GeoRow is one ASN/country tuple for a resolved IP.
type GeoRow struct {
	IP          string `json:"ip"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
	Region      string `json:"region,omitempty"`
	City        string `json:"city,omitempty"`
	ASN         uint32 `json:"asn,omitempty"`
	ASNOrg      string `json:"asn_org,omitempty"`
}

// Aggregates is precomputed for the dashboard so the static renderer doesn't
// have to walk every record. Updated by the runner just before write.
type Aggregates struct {
	SPsTotal           int            `json:"sps_total"`
	SPsReachable       int            `json:"sps_reachable"`
	SPsBySoftware      map[string]int `json:"sps_by_software"`      // detect.Software → count
	SPsByCountry       map[string]int `json:"sps_by_country"`       // ISO country code → count
	SPsByASN           map[string]int `json:"sps_by_asn"`           // "AS<n>" → count

	// Operator-level rollups (after dedup clustering)
	OperatorsTotal      int `json:"operators_total"`
	OperatorsReachable  int `json:"operators_reachable"` // operator with at least 1 reachable member
	DedupRatio          float64 `json:"dedup_ratio"`     // SPs / Operators (e.g. 3.3x means 3.3 miner IDs per real operator)

	ChainNodesTotal    int            `json:"chain_nodes_total"`
	ChainNodesBySW     map[string]int `json:"chain_nodes_by_software"`
	FoCNodesTotal      int            `json:"foc_nodes_total"`
	FoCNodesActive     int            `json:"foc_nodes_active"`
	FoCNodesReachable  int            `json:"foc_nodes_reachable"`
}

// RunStats reports collector runtime per phase.
type RunStats struct {
	StartedAt   time.Time     `json:"started_at"`
	FinishedAt  time.Time     `json:"finished_at"`
	Duration    time.Duration `json:"duration_ns"`
	PhaseTimes  map[string]time.Duration `json:"phase_times,omitempty"` // phase name → duration
	Errors      []string      `json:"errors,omitempty"` // non-fatal collector errors, capped
}

// Write atomically writes a snapshot as pretty-printed JSON.
func Write(path string, s *Snapshot) error {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	return os.Rename(tmp, path)
}

// Read parses a snapshot from disk.
func Read(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &s, nil
}
