// Package geoip enriches resolved IPs with country, ASN, and organization
// metadata.
//
// Two modes are supported:
//
//  1. MaxMind GeoLite2 (preferred when available): fully offline, free with
//     account, downloadable .mmdb files for City + ASN. Set MAXMIND_CITY_DB
//     and MAXMIND_ASN_DB env vars (or pass paths to NewMaxMind) to enable.
//
//  2. ipinfo.io (fallback): online, free tier (50k req/month), one HTTP call
//     per IP. Use NewIPInfo with an optional API token. We rate-limit at
//     the call site.
//
// The Lookup interface lets the runner swap providers without changes.
package geoip

import (
	"context"
	"net"
	"sync"
)

// Result is one enriched row per IP.
type Result struct {
	IP          string
	Country     string
	CountryCode string // ISO 3166-1 alpha-2
	Region      string
	City        string
	ASN         uint32
	ASNOrg      string
	Source      string // "maxmind" or "ipinfo" or "cache"
}

// Lookup is the geoip provider interface.
type Lookup interface {
	// Lookup returns enrichment for the given IP. ctx may be honoured for
	// network providers; offline providers ignore it.
	Lookup(ctx context.Context, ip string) (*Result, error)
	// Close releases any resources (mmdb file handles, etc.).
	Close() error
}

// Cache wraps a Lookup with an in-memory cache. Useful when the same IP
// appears across multiple records (e.g. one operator runs many SPs).
type Cache struct {
	inner Lookup
	mu    sync.RWMutex
	data  map[string]*Result
}

// NewCache wraps a Lookup with an unbounded in-memory cache. Filcensus runs
// for ~1-2h per snapshot so growth is bounded.
func NewCache(inner Lookup) *Cache {
	return &Cache{inner: inner, data: make(map[string]*Result)}
}

// Lookup checks the cache, then delegates and stores.
func (c *Cache) Lookup(ctx context.Context, ip string) (*Result, error) {
	c.mu.RLock()
	if r, ok := c.data[ip]; ok {
		c.mu.RUnlock()
		// Return a copy so callers don't accidentally mutate the cached value
		clone := *r
		clone.Source = "cache"
		return &clone, nil
	}
	c.mu.RUnlock()

	r, err := c.inner.Lookup(ctx, ip)
	if err != nil {
		return nil, err
	}
	if r != nil {
		c.mu.Lock()
		c.data[ip] = r
		c.mu.Unlock()
	}
	return r, nil
}

// Close closes the inner provider.
func (c *Cache) Close() error { return c.inner.Close() }

// IsPrivate returns true for RFC1918, loopback, link-local, ULA, etc.
// We skip these in lookups since they have no useful geo/ASN.
func IsPrivate(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return true // treat unparseable as "skip"
	}
	if parsed.IsLoopback() || parsed.IsLinkLocalUnicast() || parsed.IsLinkLocalMulticast() ||
		parsed.IsMulticast() || parsed.IsUnspecified() {
		return true
	}
	if v4 := parsed.To4(); v4 != nil {
		// RFC1918
		switch {
		case v4[0] == 10:
			return true
		case v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31:
			return true
		case v4[0] == 192 && v4[1] == 168:
			return true
		// CGNAT 100.64.0.0/10
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127:
			return true
		}
		return false
	}
	// IPv6 ULA fc00::/7
	if len(parsed) == net.IPv6len && (parsed[0]&0xfe) == 0xfc {
		return true
	}
	return false
}
