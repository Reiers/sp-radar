package geoip

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/oschwald/maxminddb-golang"
)

// MaxMind is a fully-offline geoip provider using MaxMind GeoLite2 mmdb files.
//
// Recommended databases:
//   - GeoLite2-City.mmdb  (country, region, city)
//   - GeoLite2-ASN.mmdb   (ASN + org)
//
// Both are free with a MaxMind account: https://www.maxmind.com/en/geolite2/signup
// Update with `geoipupdate` (Homebrew: brew install geoipupdate).
type MaxMind struct {
	city *maxminddb.Reader
	asn  *maxminddb.Reader
}

// NewMaxMind opens both databases. Either may be empty to skip that lookup
// (e.g. if you only have one db locally).
func NewMaxMind(cityPath, asnPath string) (*MaxMind, error) {
	m := &MaxMind{}
	if cityPath != "" {
		r, err := maxminddb.Open(cityPath)
		if err != nil {
			return nil, fmt.Errorf("open city db %s: %w", cityPath, err)
		}
		m.city = r
	}
	if asnPath != "" {
		r, err := maxminddb.Open(asnPath)
		if err != nil {
			if m.city != nil {
				m.city.Close()
			}
			return nil, fmt.Errorf("open asn db %s: %w", asnPath, err)
		}
		m.asn = r
	}
	if m.city == nil && m.asn == nil {
		return nil, fmt.Errorf("geoip/maxmind: no databases configured")
	}
	return m, nil
}

// NewMaxMindFromEnv constructs a MaxMind from MAXMIND_CITY_DB and MAXMIND_ASN_DB
// env vars. Returns nil, nil if neither is set (caller can fall back).
func NewMaxMindFromEnv() (*MaxMind, error) {
	city := os.Getenv("MAXMIND_CITY_DB")
	asn := os.Getenv("MAXMIND_ASN_DB")
	if city == "" && asn == "" {
		return nil, nil
	}
	return NewMaxMind(city, asn)
}

type cityRecord struct {
	Country struct {
		IsoCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Subdivisions []struct {
		IsoCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
}

type asnRecord struct {
	ASN uint32 `maxminddb:"autonomous_system_number"`
	Org string `maxminddb:"autonomous_system_organization"`
}

// Lookup implements Lookup.
func (m *MaxMind) Lookup(_ context.Context, ip string) (*Result, error) {
	if IsPrivate(ip) {
		return &Result{IP: ip, Source: "maxmind"}, nil
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return nil, fmt.Errorf("invalid ip %q", ip)
	}
	out := &Result{IP: ip, Source: "maxmind"}
	if m.city != nil {
		var rec cityRecord
		if err := m.city.Lookup(parsed, &rec); err != nil {
			return nil, fmt.Errorf("city lookup: %w", err)
		}
		out.CountryCode = rec.Country.IsoCode
		if n := rec.Country.Names["en"]; n != "" {
			out.Country = n
		}
		if n := rec.City.Names["en"]; n != "" {
			out.City = n
		}
		if len(rec.Subdivisions) > 0 {
			if n := rec.Subdivisions[0].Names["en"]; n != "" {
				out.Region = n
			}
		}
	}
	if m.asn != nil {
		var rec asnRecord
		if err := m.asn.Lookup(parsed, &rec); err != nil {
			return nil, fmt.Errorf("asn lookup: %w", err)
		}
		out.ASN = rec.ASN
		out.ASNOrg = rec.Org
	}
	return out, nil
}

// Close releases mmdb file handles.
func (m *MaxMind) Close() error {
	var firstErr error
	if m.city != nil {
		if err := m.city.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if m.asn != nil {
		if err := m.asn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
