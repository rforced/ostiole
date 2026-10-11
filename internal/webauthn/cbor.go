package webauthn

import (
	"errors"
	"fmt"
	"math"
	"unicode/utf8"
)

// cborMap is a decoded CBOR map. Its keys are int64 or string.
type cborMap map[any]any

const maxCBORDepth = 16

var (
	errCBORShort = errors.New("cbor: the input ends inside an item")
	errCBORRange = errors.New("cbor: integer out of range")
)

// decodeCBOR reads exactly one data item. It knows what WebAuthn uses and
// nothing more: integers, byte and text strings, arrays and maps with
// integer or text keys, all of definite length.
func decodeCBOR(b []byte) (any, error) {
	v, n, err := decodeCBORPrefix(b)
	if err != nil {
		return nil, err
	}
	if n != len(b) {
		return nil, fmt.Errorf("cbor: %d bytes after the item", len(b)-n)
	}
	return v, nil
}

// decodeCBORPrefix reads the first data item of b and reports how many bytes
// it took, which is how the key inside authenticator data is told from
// whatever follows it.
func decodeCBORPrefix(b []byte) (any, int, error) {
	r := cborReader{b: b}
	v, err := r.item(0)
	if err != nil {
		return nil, 0, err
	}
	return v, r.pos, nil
}

type cborReader struct {
	b   []byte
	pos int
}

func (r *cborReader) take(n int) ([]byte, error) {
	if n > len(r.b)-r.pos {
		return nil, errCBORShort
	}
	s := r.b[r.pos : r.pos+n]
	r.pos += n
	return s, nil
}

func (r *cborReader) argument(info byte) (uint64, error) {
	switch {
	case info < 24:
		return uint64(info), nil
	case info <= 27:
		raw, err := r.take(1 << (info - 24))
		if err != nil {
			return 0, err
		}
		var n uint64
		for _, c := range raw {
			n = n<<8 | uint64(c)
		}
		return n, nil
	case info == 31:
		return 0, errors.New("cbor: indefinite lengths are not supported")
	}
	return 0, errors.New("cbor: reserved additional information")
}

// count turns a claimed number of items or bytes into an int once the
// input is long enough to hold it, each item taking at least unit bytes.
func (r *cborReader) count(arg uint64, unit int) (int, error) {
	if arg > math.MaxInt32 {
		return 0, errCBORShort
	}
	n := int(arg)
	if n > (len(r.b)-r.pos)/unit {
		return 0, errCBORShort
	}
	return n, nil
}

func (r *cborReader) item(depth int) (any, error) {
	if depth > maxCBORDepth {
		return nil, errors.New("cbor: nested too deeply")
	}
	initial, err := r.take(1)
	if err != nil {
		return nil, err
	}
	major := initial[0] >> 5
	if major > 5 {
		return nil, fmt.Errorf("cbor: major type %d is not supported", major)
	}
	arg, err := r.argument(initial[0] & 0x1f)
	if err != nil {
		return nil, err
	}
	switch major {
	case 0:
		if arg > math.MaxInt64 {
			return nil, errCBORRange
		}
		return int64(arg), nil
	case 1:
		if arg > math.MaxInt64 {
			return nil, errCBORRange
		}
		return -1 - int64(arg), nil
	case 2, 3:
		n, err := r.count(arg, 1)
		if err != nil {
			return nil, err
		}
		raw, _ := r.take(n)
		if major == 2 {
			return raw, nil
		}
		if !utf8.Valid(raw) {
			return nil, errors.New("cbor: text is not UTF-8")
		}
		return string(raw), nil
	case 4:
		n, err := r.count(arg, 1)
		if err != nil {
			return nil, err
		}
		list := make([]any, 0, n)
		for range n {
			v, err := r.item(depth + 1)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		return list, nil
	}
	n, err := r.count(arg, 2)
	if err != nil {
		return nil, err
	}
	m := make(cborMap, n)
	for range n {
		k, err := r.item(depth + 1)
		if err != nil {
			return nil, err
		}
		switch k.(type) {
		case int64, string:
		default:
			return nil, errors.New("cbor: a map key is neither an integer nor text")
		}
		if _, dup := m[k]; dup {
			return nil, fmt.Errorf("cbor: map key %v appears twice", k)
		}
		v, err := r.item(depth + 1)
		if err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, nil
}
