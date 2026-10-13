package httpapi

import (
	"encoding/base64"
	"errors"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	for _, seq := range []int64{2, 3, 50, 1 << 40} {
		got, err := decodeCursor(encodeCursor(seq))
		if err != nil || got != seq {
			t.Errorf("round trip %d: got %d, %v", seq, got, err)
		}
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for name, cursor := range map[string]string{
		"not base64":         "%%%",
		"wrong prefix":       b64("page:5"),
		"no number":          b64("seq:"),
		"not a number":       b64("seq:abc"),
		"seq 1 (empty page)": b64("seq:1"),
		"seq 0":              b64("seq:0"),
		"negative":           b64("seq:-5"),
		"leading zero":       b64("seq:05"),
		"plus sign":          b64("seq:+5"),
		"overflow":           b64("seq:9223372036854775808"),
		"padded base64":      base64.URLEncoding.EncodeToString([]byte("seq:5")),
	} {
		if _, err := decodeCursor(cursor); !errors.Is(err, errBadCursor) {
			t.Errorf("%s (%q): want errBadCursor, got %v", name, cursor, err)
		}
	}
}
