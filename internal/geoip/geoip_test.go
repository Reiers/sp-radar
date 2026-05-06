package geoip

import (
	"context"
	"errors"
	"testing"
)

func TestIsPrivate(t *testing.T) {
	cases := map[string]bool{
		"10.0.0.1":         true,
		"172.16.5.4":       true,
		"172.31.255.255":   true,
		"172.32.0.0":       false,
		"192.168.1.1":      true,
		"100.64.0.1":       true,
		"127.0.0.1":        true,
		"::1":              true,
		"fc00::1":          true,
		"8.8.8.8":          false,
		"1.1.1.1":          false,
		"2001:4860:4860::8888": false,
		"":                 true,
		"not-an-ip":        true,
	}
	for ip, want := range cases {
		if got := IsPrivate(ip); got != want {
			t.Errorf("IsPrivate(%q): got %v, want %v", ip, got, want)
		}
	}
}

// stubLookup is used to test the Cache wrapper.
type stubLookup struct {
	calls map[string]int
	err   error
}

func (s *stubLookup) Lookup(_ context.Context, ip string) (*Result, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.calls == nil {
		s.calls = make(map[string]int)
	}
	s.calls[ip]++
	return &Result{IP: ip, CountryCode: "XX", Source: "stub"}, nil
}

func (s *stubLookup) Close() error { return nil }

func TestCache_HitsAndMisses(t *testing.T) {
	stub := &stubLookup{}
	c := NewCache(stub)

	// First lookup → miss (1 call)
	r1, err := c.Lookup(context.Background(), "8.8.8.8")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r1.Source != "stub" {
		t.Errorf("first lookup should be stub, got %q", r1.Source)
	}

	// Second lookup → cache hit (still 1 call)
	r2, err := c.Lookup(context.Background(), "8.8.8.8")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r2.Source != "cache" {
		t.Errorf("second lookup should be cache, got %q", r2.Source)
	}
	if stub.calls["8.8.8.8"] != 1 {
		t.Errorf("expected 1 underlying call, got %d", stub.calls["8.8.8.8"])
	}

	// Different IP → miss
	if _, err := c.Lookup(context.Background(), "1.1.1.1"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if stub.calls["1.1.1.1"] != 1 {
		t.Errorf("expected 1 underlying call for 1.1.1.1, got %d", stub.calls["1.1.1.1"])
	}
}

func TestCache_DoesNotCacheErrors(t *testing.T) {
	stub := &stubLookup{err: errors.New("boom")}
	c := NewCache(stub)

	if _, err := c.Lookup(context.Background(), "8.8.8.8"); err == nil {
		t.Fatal("expected error")
	}
	// Now succeed
	stub.err = nil
	r, err := c.Lookup(context.Background(), "8.8.8.8")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r.Source == "cache" {
		t.Errorf("error should not have been cached")
	}
}
