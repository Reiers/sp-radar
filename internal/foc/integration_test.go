package foc_test

import (
	"github.com/Reiers/sp-radar/internal/foc"
	"github.com/Reiers/sp-radar/internal/scanner"
)

// Compile-time check: *scanner.LotusRPC must satisfy foc.EthCaller.
// If this stops compiling, EthCall signatures are out of sync.
var _ foc.EthCaller = (*scanner.LotusRPC)(nil)
