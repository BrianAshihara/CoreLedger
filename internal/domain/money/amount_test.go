package money

import (
	"errors"
	"math"
	"testing"
)

func TestValidateAmount(t *testing.T) {
	for _, ok := range []int64{1, 1050, MaxAmount} {
		if err := ValidateAmount(ok); err != nil {
			t.Errorf("ValidateAmount(%d) = %v", ok, err)
		}
	}
	for _, bad := range []int64{0, -1, MaxAmount + 1, math.MinInt64, math.MaxInt64} {
		if err := ValidateAmount(bad); !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("ValidateAmount(%d) = %v, want ErrInvalidAmount", bad, err)
		}
	}
}

func TestAdd(t *testing.T) {
	for _, tc := range []struct {
		a, b, want int64
		overflow   bool
	}{
		{1, 2, 3, false},
		{-5, 3, -2, false},
		{math.MaxInt64, 0, math.MaxInt64, false},
		{math.MaxInt64 - 1, 1, math.MaxInt64, false},
		{math.MinInt64 + 1, -1, math.MinInt64, false},
		{math.MaxInt64, -1, math.MaxInt64 - 1, false},
		{math.MaxInt64, 1, 0, true},
		{math.MinInt64, -1, 0, true},
		{math.MaxInt64, math.MaxInt64, 0, true},
		{math.MinInt64, math.MinInt64, 0, true},
	} {
		got, err := Add(tc.a, tc.b)
		if tc.overflow {
			if !errors.Is(err, ErrOverflow) {
				t.Errorf("Add(%d, %d): want ErrOverflow, got %d, %v", tc.a, tc.b, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("Add(%d, %d) = %d, %v; want %d", tc.a, tc.b, got, err, tc.want)
		}
	}
}
