package indexformula

import "testing"

func TestContribution(t *testing.T) {
	tests := []struct {
		name string
		in   Counts
		want float64
	}{
		{name: "quiet"},
		{name: "regular issue", in: Counts{RegularIssuesCreated: 2}, want: 2},
		{name: "regular merged pr", in: Counts{RegularPRsMerged: 2}, want: 6},
		{name: "clanker created", in: Counts{ClankerPRsCreated: 2}, want: 10},
		{name: "idea filed", in: Counts{IdeasFiled: 2}, want: 16},
		{name: "clanker merged", in: Counts{ClankerPRsMerged: 2}, want: 20},
		{name: "composite", in: Counts{RegularIssuesCreated: 1, RegularPRsMerged: 1, ClankerPRsCreated: 1, IdeasFiled: 1, ClankerPRsMerged: 1}, want: 27},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Contribution(tt.in); got != tt.want {
				t.Fatalf("Contribution(%+v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
