package server

import (
	"encoding/json"
	"os"
	"testing"
)

const dashboardPath = "../../docs/grafana/dibs-dashboard.json"

// The Grafana dashboard is not schema-checked by CI; pin the metric names,
// label names and by-clauses its queries use to what /metrics emits.
func TestDashboardQueriesSelectEmittedMetrics(t *testing.T) {
	raw, err := os.ReadFile(dashboardPath)
	if err != nil {
		t.Fatalf("read %s: %v", dashboardPath, err)
	}
	var d struct {
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("parse %s: %v", dashboardPath, err)
	}
	if len(d.Panels) == 0 {
		t.Fatal("dashboard has no panels")
	}
	ex := scrapeAllFamilies(t)
	for _, p := range d.Panels {
		for _, tg := range p.Targets {
			sels := selectorRe.FindAllStringSubmatch(tg.Expr, -1)
			if len(sels) == 0 {
				t.Errorf("%s: query selects no dibs_ metric: %s", p.Title, tg.Expr)
			}
			for _, sel := range sels {
				labels, ok := ex[sel[1]]
				if !ok {
					t.Errorf("%s: selects %s, which /metrics never emits", p.Title, sel[1])
					continue
				}
				for _, m := range matcherRe.FindAllStringSubmatch(sel[2], -1) {
					if _, ok := labels[m[1]]; !ok {
						t.Errorf("%s: %s has no label %q", p.Title, sel[1], m[1])
					}
				}
				for _, bc := range byClauseRe.FindAllStringSubmatch(tg.Expr, -1) {
					for _, by := range splitLabelList(bc[1]) {
						if by == "le" {
							continue
						}
						if _, ok := labels[by]; !ok {
							t.Errorf("%s: aggregates by %q but %s has no such label", p.Title, by, sel[1])
						}
					}
				}
			}
		}
	}
}
