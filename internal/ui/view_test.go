package ui

import "testing"

func TestScrollWindow(t *testing.T) {
	cases := []struct{ n, cursor, h, lo, hi int }{
		{40, -1, 10, 0, 10}, // no selection: top
		{40, 0, 10, 0, 10},
		{40, 9, 10, 0, 10},  // last visible row
		{40, 10, 10, 1, 11}, // scrolled by one
		{40, 39, 10, 30, 40},
		{5, 4, 10, 0, 5}, // fewer rows than lines
	}
	for _, c := range cases {
		if lo, hi := scrollWindow(c.n, c.cursor, c.h); lo != c.lo || hi != c.hi {
			t.Errorf("scrollWindow(%d,%d,%d) = %d,%d want %d,%d", c.n, c.cursor, c.h, lo, hi, c.lo, c.hi)
		}
	}
}
