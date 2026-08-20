// Package indexformula defines the repo market index's weighted composite.
package indexformula

const (
	WeightRegularIssueCreated = 1.0
	WeightRegularPRMerged     = 3.0
	WeightClankerPRCreated    = 5.0
	WeightIdeaFiled           = 8.0
	WeightClankerPRMerged     = 10.0
)

// Counts is the daily activity vector used by the repo market index.
type Counts struct {
	RegularIssuesCreated int
	RegularPRsMerged     int
	ClankerPRsCreated    int
	IdeasFiled           int
	ClankerPRsMerged     int
}

// Contribution returns one day's index movement:
//
//	1·issue + 3·PR merged + 5·clanker PR + 8·idea filed + 10·clanker merged
//
// Keep all chart, ticker, live, and backfilled repo movement routed through
// this function so changing the composite cannot split semantics by source.
func Contribution(c Counts) float64 {
	return float64(c.RegularIssuesCreated)*WeightRegularIssueCreated +
		float64(c.RegularPRsMerged)*WeightRegularPRMerged +
		float64(c.ClankerPRsCreated)*WeightClankerPRCreated +
		float64(c.IdeasFiled)*WeightIdeaFiled +
		float64(c.ClankerPRsMerged)*WeightClankerPRMerged
}

// Activity returns the unweighted number of source events in c.
func Activity(c Counts) int {
	return c.RegularIssuesCreated + c.RegularPRsMerged + c.ClankerPRsCreated + c.IdeasFiled + c.ClankerPRsMerged
}
