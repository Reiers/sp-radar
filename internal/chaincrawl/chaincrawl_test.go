package chaincrawl

import (
	"reflect"
	"sort"
	"testing"
)

func TestSplitMaddr(t *testing.T) {
	cases := map[string][]string{
		"/ip4/8.8.8.8/tcp/1234":      {"ip4", "8.8.8.8", "tcp", "1234"},
		"/ip6/::1/tcp/1234":          {"ip6", "::1", "tcp", "1234"},
		"/dns4/example.com/tcp/443":  {"dns4", "example.com", "tcp", "443"},
		"":                           nil,
		"no-leading-slash":           nil,
	}
	for in, want := range cases {
		got := splitMaddr(in)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("splitMaddr(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIPsFromMultiaddrStrings(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{
			in:   []string{"/ip4/8.8.8.8/tcp/1234"},
			want: []string{"8.8.8.8"},
		},
		{
			in: []string{
				"/ip4/8.8.8.8/tcp/1234",
				"/ip4/8.8.8.8/udp/9000/quic",
				"/ip6/2001:db8::1/tcp/1234",
				"/dnsaddr/example.com/tcp/443",
			},
			want: []string{"2001:db8::1", "8.8.8.8"},
		},
		{
			in:   nil,
			want: nil,
		},
	}
	for _, tc := range cases {
		got := ipsFromMultiaddrStrings(tc.in)
		sort.Strings(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("got %v, want %v", got, tc.want)
		}
	}
}
