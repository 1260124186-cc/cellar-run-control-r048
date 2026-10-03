package service

import (
	"fmt"
	"sort"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// GenerateRunReport archives a terminal run into an immutable completion
// report. Generation is idempotent per run: the first caller creates the
// report and every later caller receives the same persisted report, so
// concurrent requests can never produce two contradictory archives. The
// report body, its run index entry, and the number sequence are committed in
// a single snapshot write, so a failed write leaves no index without a body.
func (s *Service) GenerateRunReport(runID string) (domain.CompletionReport, bool, error) {
	var existing domain.CompletionReport
	found := false
	err := s.store.View(func(snapshot domain.Snapshot) error {
		number, ok := snapshot.ReportRunIndex[runID]
		if !ok {
			return nil
		}
		report, ok := snapshot.Reports[number]
		if !ok {
			return domain.NewError(domain.CodeStorage,
				"report index references a missing report")
		}
		existing = report.Clone()
		found = true
		return nil
	})
	if err != nil {
		return domain.CompletionReport{}, false, err
	}
	if found {
		return existing, false, nil
	}

	var report domain.CompletionReport
	created := false
	err = s.store.Execute(func(snapshot *domain.Snapshot) error {
		if number, ok := snapshot.ReportRunIndex[runID]; ok {
			persisted, ok := snapshot.Reports[number]
			if !ok {
				return domain.NewError(domain.CodeStorage,
					"report index references a missing report")
			}
			report = persisted.Clone()
			return nil
		}
		run, ok := snapshot.Runs[runID]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		formula, ok := snapshot.Formulas[run.FormulaID]
		if !ok {
			return domain.NewError(domain.CodeFormulaNotFound,
				"attached formula was not found")
		}
		var vessel *domain.Vessel
		if run.VesselID != nil {
			value, ok := snapshot.Vessels[*run.VesselID]
			if !ok {
				return domain.NewError(domain.CodeVesselNotFound,
					"attached vessel was not found")
			}
			vessel = &value
		}
		next := snapshot.ReportSeq + 1
		built, err := domain.NewCompletionReport(
			fmt.Sprintf("RPT-%06d", next), run, formula, vessel, s.clock.Now())
		if err != nil {
			return err
		}
		snapshot.ReportSeq = next
		snapshot.Reports[built.Number] = built.Clone()
		snapshot.ReportRunIndex[runID] = built.Number
		report = built.Clone()
		created = true
		return nil
	})
	if err != nil {
		return domain.CompletionReport{}, false, err
	}
	return report, created, nil
}

func (s *Service) GetReport(number string) (domain.CompletionReport, error) {
	normalized := domain.NormalizeReportNumber(number)
	var found domain.CompletionReport
	err := s.store.View(func(snapshot domain.Snapshot) error {
		report, ok := snapshot.Reports[normalized]
		if !ok {
			return domain.NewError(domain.CodeReportNotFound, "report was not found")
		}
		found = report.Clone()
		return nil
	})
	if err != nil {
		return domain.CompletionReport{}, err
	}
	return found, nil
}

func (s *Service) ListReports() ([]domain.ReportSummary, error) {
	var summaries []domain.ReportSummary
	err := s.store.View(func(snapshot domain.Snapshot) error {
		summaries = make([]domain.ReportSummary, 0, len(snapshot.Reports))
		for _, report := range snapshot.Reports {
			summaries = append(summaries, report.Summary())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Number < summaries[j].Number
	})
	return summaries, nil
}
