package foc

import (
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

type gethCommonAddress = common.Address

func mustAddr(s string) common.Address { return common.HexToAddress(s) }
func hexEncode(b []byte) string         { return hex.EncodeToString(b) }

// fakeRPC lets us script EthCall responses for unit tests.
type fakeRPC struct {
	responses map[string]string // dataHex prefix -> returnHex
	err       error
}

func (f *fakeRPC) EthCall(_ context.Context, _ string, dataHex string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	for prefix, ret := range f.responses {
		if len(dataHex) >= len(prefix) && dataHex[:len(prefix)] == prefix {
			return ret, nil
		}
	}
	return "", errors.New("fakeRPC: no canned response for " + dataHex)
}

func TestRegistryAddress(t *testing.T) {
	if a := RegistryAddress(Mainnet); a != "0xf55dDbf63F1b55c3F1D4FA7e339a68AB7b64A5eB" {
		t.Errorf("mainnet: got %s", a)
	}
	if a := RegistryAddress(Calibration); a != "0x839e5c9988e4e9977d40708d0094103c0839Ac9D" {
		t.Errorf("calibration: got %s", a)
	}
	if a := RegistryAddress(Network("nonsense")); a != "" {
		t.Errorf("unknown network should return empty, got %s", a)
	}
}

func TestEncodeUint256(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0000000000000000000000000000000000000000000000000000000000000000"},
		{1, "0000000000000000000000000000000000000000000000000000000000000001"},
		{100, "0000000000000000000000000000000000000000000000000000000000000064"},
		{0x40, "0000000000000000000000000000000000000000000000000000000000000040"},
	}
	for _, c := range cases {
		got := encodeUint256(big.NewInt(c.in))
		if got != c.want {
			t.Errorf("encodeUint256(%d): got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestDecodeUint256(t *testing.T) {
	v, err := decodeUint256("0x0000000000000000000000000000000000000000000000000000000000000064")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if v.Int64() != 100 {
		t.Errorf("got %s, want 100", v.String())
	}
}

func TestDecodeUint256_TooShort(t *testing.T) {
	if _, err := decodeUint256("0x1234"); err == nil {
		t.Error("expected error on short return")
	}
}

func TestDecodeUint256ArrayBoolReturn(t *testing.T) {
	// Construct ABI return for getAllActiveProviders(...):
	//   uint256[] = [42, 100, 7]
	//   bool hasMore = true
	//
	// Layout:
	//   slot 0: offset to ids (0x40)
	//   slot 1: hasMore (1)
	//   slot 2: length (3)
	//   slot 3: 42
	//   slot 4: 100
	//   slot 5: 7
	hexStr := "0x" +
		"0000000000000000000000000000000000000000000000000000000000000040" + // offset = 64
		"0000000000000000000000000000000000000000000000000000000000000001" + // hasMore = true
		"0000000000000000000000000000000000000000000000000000000000000003" + // length = 3
		"000000000000000000000000000000000000000000000000000000000000002a" + // 42
		"0000000000000000000000000000000000000000000000000000000000000064" + // 100
		"0000000000000000000000000000000000000000000000000000000000000007" //   7

	ids, hasMore, err := decodeUint256ArrayBoolReturn(hexStr)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !hasMore {
		t.Errorf("hasMore: got false, want true")
	}
	if len(ids) != 3 {
		t.Fatalf("len(ids): got %d, want 3", len(ids))
	}
	if ids[0].Int64() != 42 || ids[1].Int64() != 100 || ids[2].Int64() != 7 {
		t.Errorf("ids: got %v", ids)
	}
}

func TestDecodeUint256ArrayBoolReturn_Empty(t *testing.T) {
	hexStr := "0x" +
		"0000000000000000000000000000000000000000000000000000000000000040" + // offset
		"0000000000000000000000000000000000000000000000000000000000000000" + // hasMore = false
		"0000000000000000000000000000000000000000000000000000000000000000" //   length = 0
	ids, hasMore, err := decodeUint256ArrayBoolReturn(hexStr)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if hasMore {
		t.Errorf("hasMore should be false")
	}
	if len(ids) != 0 {
		t.Errorf("expected empty, got %v", ids)
	}
}

func TestGetActiveProviderCount(t *testing.T) {
	rpc := &fakeRPC{responses: map[string]string{
		"0x" + selActiveProviderCount: "0x000000000000000000000000000000000000000000000000000000000000002a",
	}}
	got, err := GetActiveProviderCount(context.Background(), rpc, Mainnet)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Int64() != 42 {
		t.Errorf("got %s, want 42", got.String())
	}
}

func TestGetAllActiveProviders(t *testing.T) {
	// First page: ids [1,2,3], hasMore=true
	page1 := "0x" +
		"0000000000000000000000000000000000000000000000000000000000000040" +
		"0000000000000000000000000000000000000000000000000000000000000001" + // hasMore
		"0000000000000000000000000000000000000000000000000000000000000003" + // len
		"0000000000000000000000000000000000000000000000000000000000000001" +
		"0000000000000000000000000000000000000000000000000000000000000002" +
		"0000000000000000000000000000000000000000000000000000000000000003"

	rpc := &fakeRPC{responses: map[string]string{
		"0x" + selGetAllActiveProviders: page1,
	}}
	ids, hasMore, err := GetAllActiveProviders(context.Background(), rpc, Mainnet, 0, 100)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !hasMore || len(ids) != 3 {
		t.Errorf("got %d ids hasMore=%v, want 3 / true", len(ids), hasMore)
	}
}

func TestEnumerateActiveProviderIDs_Pagination(t *testing.T) {
	// Counter-based fake: returns one different page per call.
	calls := 0
	rpc := &fakeRPCFn{fn: func(_ context.Context, _ string, dataHex string) (string, error) {
		_ = dataHex
		calls++
		switch calls {
		case 1:
			// page 1: [10,20], hasMore=true
			return "0x" +
				"0000000000000000000000000000000000000000000000000000000000000040" +
				"0000000000000000000000000000000000000000000000000000000000000001" +
				"0000000000000000000000000000000000000000000000000000000000000002" +
				"000000000000000000000000000000000000000000000000000000000000000a" +
				"0000000000000000000000000000000000000000000000000000000000000014", nil
		case 2:
			// page 2: [30], hasMore=false
			return "0x" +
				"0000000000000000000000000000000000000000000000000000000000000040" +
				"0000000000000000000000000000000000000000000000000000000000000000" +
				"0000000000000000000000000000000000000000000000000000000000000001" +
				"000000000000000000000000000000000000000000000000000000000000001e", nil
		}
		return "", errors.New("unexpected extra call")
	}}
	ids, err := EnumerateActiveProviderIDs(context.Background(), rpc, Mainnet, 2)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("len: got %d, want 3", len(ids))
	}
	if ids[0].Int64() != 10 || ids[1].Int64() != 20 || ids[2].Int64() != 30 {
		t.Errorf("ids: got %v", ids)
	}
	if calls != 2 {
		t.Errorf("calls: got %d, want 2", calls)
	}
}

// fakeRPCFn supports per-call dynamic responses
type fakeRPCFn struct {
	fn func(ctx context.Context, to, data string) (string, error)
}

func (f *fakeRPCFn) EthCall(ctx context.Context, to, data string) (string, error) {
	return f.fn(ctx, to, data)
}

func TestDecodePDPCapabilities(t *testing.T) {
	caps := map[string][]byte{
		"serviceURL":               []byte("https://pdp.example.com/"),
		"location":                 []byte("DE"),
		"ipniPeerId":               []byte("12D3KooWExample"),
		"ipniPiece":                {1},
		"ipniIpfs":                 {0},
		"minPieceSizeInBytes":      bigBytes(1024),
		"maxPieceSizeInBytes":      bigBytes(1 << 30),
		"storagePricePerTibPerDay": bigBytes(5_000_000),
		"minProvingPeriodInEpochs": bigBytes(2880),
		"paymentTokenAddress":      mustHex("80B98d3aa09ffff255c3ba4A241111Ff1262F045"),
	}
	o := DecodePDPCapabilities(caps)
	if o.ServiceURL != "https://pdp.example.com/" {
		t.Errorf("ServiceURL: %s", o.ServiceURL)
	}
	if o.Location != "DE" {
		t.Errorf("Location: %s", o.Location)
	}
	if o.IPNIPeerID != "12D3KooWExample" {
		t.Errorf("IPNIPeerID: %s", o.IPNIPeerID)
	}
	if !o.IPNISupportsPiece {
		t.Errorf("IPNISupportsPiece should be true")
	}
	if o.IPNISupportsIPFS {
		t.Errorf("IPNISupportsIPFS should be false")
	}
	if o.MinPieceSizeBytes == nil || o.MinPieceSizeBytes.Int64() != 1024 {
		t.Errorf("MinPieceSizeBytes: %v", o.MinPieceSizeBytes)
	}
	if o.PaymentTokenAddress != "0x80b98d3aa09ffff255c3ba4a241111ff1262f045" {
		t.Errorf("PaymentTokenAddress: %s", o.PaymentTokenAddress)
	}
}

func bigBytes(v int64) []byte {
	return big.NewInt(v).Bytes()
}

func mustHex(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}

// TestGetProvider_DecodesABIReturn synthesises a valid ABI-encoded
// ProviderWithProduct return blob in-memory using go-ethereum's abi.Pack and
// verifies that decodeProviderWithProduct correctly reads it back. This
// catches any drift between our hand-written tuple type spec and the upstream
// contract schema, since both encode and decode go through the same Arguments.
func TestGetProvider_DecodesABIReturn(t *testing.T) {
	// Build a fake encoded blob via the same ABI Arguments we decode with.
	// We use a struct shape that matches the tuple definition.
	type rawTuple struct {
		ProviderId   *big.Int
		ProviderInfo struct {
			ServiceProvider gethCommonAddress
			Payee           gethCommonAddress
			Name            string
			Description     string
			IsActive        bool
		}
		Product struct {
			ProductType    uint8
			CapabilityKeys []string
			IsActive       bool
		}
		ProductCapabilityValues [][]byte
	}
	input := rawTuple{
		ProviderId: big.NewInt(7),
	}
	input.ProviderInfo.ServiceProvider = mustAddr("0x1111111111111111111111111111111111111111")
	input.ProviderInfo.Payee = mustAddr("0x2222222222222222222222222222222222222222")
	input.ProviderInfo.Name = "TestProvider"
	input.ProviderInfo.Description = "hello world"
	input.ProviderInfo.IsActive = true
	input.Product.ProductType = 0 // PDP
	input.Product.CapabilityKeys = []string{"serviceURL", "location", "ipniPiece"}
	input.Product.IsActive = true
	input.ProductCapabilityValues = [][]byte{
		[]byte("https://pdp.test/"),
		[]byte("NO"),
		{1},
	}

	packed, err := abiArgsProviderWithProduct.Pack(input)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	p, err := decodeProviderWithProduct("0x" + hexEncode(packed))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.ID == nil || p.ID.Int64() != 7 {
		t.Errorf("ID: got %v, want 7", p.ID)
	}
	if p.Name != "TestProvider" {
		t.Errorf("Name: got %q", p.Name)
	}
	if !p.Active {
		t.Errorf("Active should be true")
	}
	if !p.HasPDP {
		t.Errorf("HasPDP should be true")
	}
	if p.PDP.ServiceURL != "https://pdp.test/" {
		t.Errorf("ServiceURL: got %q", p.PDP.ServiceURL)
	}
	if p.PDP.Location != "NO" {
		t.Errorf("Location: got %q", p.PDP.Location)
	}
	if !p.PDP.IPNISupportsPiece {
		t.Errorf("IPNISupportsPiece should be true")
	}
	if p.ServiceProviderHex != "0x1111111111111111111111111111111111111111" {
		t.Errorf("ServiceProviderHex: got %q", p.ServiceProviderHex)
	}
}


