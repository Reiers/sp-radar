package cluster

import "math/big"

// bigSum is a tiny helper for accumulating big-int decimal strings.
// Avoids spreading math/big arithmetic through Build().
type bigSum struct {
	v *big.Int
}

func (b *bigSum) Add(s string) {
	if b.v == nil {
		b.v = new(big.Int)
	}
	if s == "" {
		return
	}
	x, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return
	}
	b.v.Add(b.v, x)
}

func (b *bigSum) String() string {
	if b.v == nil {
		return "0"
	}
	return b.v.String()
}

// bigCmp compares two big-int decimal strings; returns -1/0/1.
// Empty / unparseable strings are treated as 0.
func bigCmp(a, b string) int {
	parse := func(s string) *big.Int {
		if s == "" {
			return new(big.Int)
		}
		x, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return new(big.Int)
		}
		return x
	}
	return parse(a).Cmp(parse(b))
}
