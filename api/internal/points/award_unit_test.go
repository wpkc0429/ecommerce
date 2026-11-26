package points

import (
	"math"
	"testing"
)

func TestAwardFor(t *testing.T) {
	cases := []struct {
		in   int64
		want int32
	}{
		{0, 0}, {99, 0}, {100, 1}, {12345, 123},
		{int64(math.MaxInt32) * pointsPerMinorUnit, math.MaxInt32},
		{math.MaxInt64, math.MaxInt32},
		{math.MinInt64, math.MinInt32},
	}
	for _, c := range cases {
		if got := awardFor(c.in); got != c.want {
			t.Errorf("awardFor(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
