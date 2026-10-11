package webauthn

import (
	"bytes"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The examples are from RFC 8949, Appendix A.
func TestDecodeCBOR(t *testing.T) {
	cases := []struct {
		hex  string
		want any
	}{
		{"00", int64(0)},
		{"17", int64(23)},
		{"1818", int64(24)},
		{"1903e8", int64(1000)},
		{"1a000f4240", int64(1000000)},
		{"1b000000e8d4a51000", int64(1000000000000)},
		{"1b7fffffffffffffff", int64(1<<63 - 1)},
		{"20", int64(-1)},
		{"3863", int64(-100)},
		{"3903e7", int64(-1000)},
		{"3b7fffffffffffffff", int64(-1 << 63)},
		{"40", []byte{}},
		{"4401020304", []byte{1, 2, 3, 4}},
		{"60", ""},
		{"6449455446", "IETF"},
		{"80", []any{}},
		{"83010203", []any{int64(1), int64(2), int64(3)}},
		{"a0", cborMap{}},
		{"a201020304", cborMap{int64(1): int64(2), int64(3): int64(4)}},
		{"a26161016162820203", cborMap{"a": int64(1), "b": []any{int64(2), int64(3)}}},
	}
	for _, tc := range cases {
		got, err := decodeCBOR(unhex(t, tc.hex))
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %#v, %v, want %#v", tc.hex, got, err, tc.want)
		}
	}
}

func TestDecodeCBORRefuses(t *testing.T) {
	cases := []struct{ name, hex string }{
		{"indefinite byte string", "5f42010243030405ff"},
		{"indefinite text string", "7f657374726561646d696e67ff"},
		{"indefinite array", "9f018202039f0405ffff"},
		{"indefinite map", "bf61610161629f0203ffff"},
		{"tag", "c11a514b67b0"},
		{"float", "f93c00"},
		{"simple value", "f5"},
		{"stray break", "ff"},
		{"reserved additional information", "1c"},
		{"empty input", ""},
		{"trailing bytes", "0000"},
		{"string cut short", "440102"},
		{"string longer than the input", "5affffffff00"},
		{"array longer than the input", "9bffffffffffffffff"},
		{"map longer than the input", "baffffffff00"},
		{"unsigned beyond int64", "1b8000000000000000"},
		{"negative beyond int64", "3b8000000000000000"},
		{"map key that is a byte string", "a1410000"},
		{"map key that is an array", "a18000"},
		{"map key twice", "a201020103"},
		{"text that is not UTF-8", "62c328"},
		{"nested too deeply", strings.Repeat("81", 40) + "00"},
	}
	for _, tc := range cases {
		if got, err := decodeCBOR(unhex(t, tc.hex)); err == nil {
			t.Errorf("%s: decoded to %#v", tc.name, got)
		}
	}
}

func TestDecodeCBORPrefixSaysWhereTheItemEnds(t *testing.T) {
	_, n, err := decodeCBORPrefix(unhex(t, "a101020a"))
	if err != nil || n != 3 {
		t.Errorf("got %d, %v, want 3", n, err)
	}
	if got := bytes.Equal(unhex(t, "a101020a")[:n], unhex(t, "a10102")); !got {
		t.Error("the item is not the first three bytes")
	}
}

func FuzzDecodeCBOR(f *testing.F) {
	for _, h := range []string{"a201020304", "a26161016162820203", "5f42010243030405ff", "a1410000", "9bffffffffffffffff", "62c328"} {
		b, _ := hex.DecodeString(h)
		f.Add(b)
	}
	f.Fuzz(func(_ *testing.T, b []byte) {
		_, _ = decodeCBOR(b)
		_, _ = parseCOSEKey(b)
	})
}
