package spscan

import (
	"reflect"
	"sort"
	"testing"

	"github.com/multiformats/go-multiaddr"
)

func TestIPsFromMultiaddrs(t *testing.T) {
	cases := []struct {
		name  string
		addrs []string
		want  []string
	}{
		{
			name:  "single ipv4",
			addrs: []string{"/ip4/8.8.8.8/tcp/1234"},
			want:  []string{"8.8.8.8"},
		},
		{
			name: "v4+v6 dedup",
			addrs: []string{
				"/ip4/8.8.8.8/tcp/1234",
				"/ip4/8.8.8.8/tcp/4321",
				"/ip6/2001:db8::1/tcp/1234",
			},
			want: []string{"2001:db8::1", "8.8.8.8"},
		},
		{
			name:  "empty",
			addrs: nil,
			want:  nil,
		},
		{
			name: "dnsaddr (no IP component)",
			addrs: []string{
				"/dnsaddr/example.com/tcp/443",
			},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mas []multiaddr.Multiaddr
			for _, s := range tc.addrs {
				ma, err := multiaddr.NewMultiaddr(s)
				if err != nil {
					t.Fatalf("bad addr %q: %v", s, err)
				}
				mas = append(mas, ma)
			}
			got := ipsFromMultiaddrs(mas)
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
