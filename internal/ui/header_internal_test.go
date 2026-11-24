package ui

import "testing"

func TestHeaderLayout(t *testing.T) {
	cols := []int{20, 20, 20}
	tests := []struct {
		width    int
		wantCols int
		wantLogo bool
	}{
		{200, 3, true},  // everything fits
		{110, 3, true},  // exactly fits
		{100, 3, false}, // logo goes first
		{75, 2, false},  // then hint columns from the right
		{50, 1, false},
		{49, 0, false},
		{20, 0, false}, // narrower than the info block itself
		{0, 0, false},
	}
	for _, tt := range tests {
		gotCols, gotLogo := headerLayout(tt.width, 30, cols, 20)
		if gotCols != tt.wantCols || gotLogo != tt.wantLogo {
			t.Errorf("headerLayout(%d) = (%d, %v), want (%d, %v)",
				tt.width, gotCols, gotLogo, tt.wantCols, tt.wantLogo)
		}
	}
}

func TestHeaderLayout_noHints(t *testing.T) {
	if cols, logo := headerLayout(60, 30, nil, 20); cols != 0 || !logo {
		t.Errorf("headerLayout with no hint columns = (%d, %v), want (0, true)", cols, logo)
	}
}
