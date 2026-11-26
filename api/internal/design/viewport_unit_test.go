package design

import (
	"math"
	"testing"
)

func TestViewportInt16(t *testing.T) {
	for _, v := range []int{390, 1280, 0, math.MaxInt16, math.MinInt16} {
		if got := viewportInt16(v); int(got) != v {
			t.Errorf("viewportInt16(%d) = %d", v, got)
		}
	}
	for _, v := range []int{math.MaxInt16 + 1, math.MinInt16 - 1, math.MaxInt32} {
		if got := viewportInt16(v); got != 0 {
			t.Errorf("viewportInt16(%d) = %d, want 0", v, got)
		}
	}
}
