package service

import (
	"errors"
	"sort"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

// GenerateCompletionReport freezes the completion dossier for a run. Reports
// are idempotent per run: a repeat generation returns the already archived
// report with existing set to true instead of creating a second, potentially
// different, archive.
func (s *Service) GenerateCompletionReport(runID string) (report domain.CompletionReport, existing bool, err error) {
	generatedAt := s.clock.Now()
	report, err = s.store.GenerateReport(
		func(snapshot domain.Snapshot, nextRevision int64) (domain.CompletionReport, error) {
			run, ok := snapshot.Runs[runID]
			if !ok {
				return domain.CompletionReport{}, domain.NewError(domain.CodeRunNotFound,
					"run was not found")
			}
			if run.State != domain.RunCompleted && run.State != domain.RunAborted {
				return domain.CompletionReport{}, domain.NewError(domain.CodeRunState,
					"a report can only be generated for a completed or aborted run")
			}
			formula, ok := snapshot.Formulas[run.FormulaID]
			if !ok {
				return domain.CompletionReport{}, domain.NewError(domain.CodeFormulaNotFound,
					"attached formula was not found")
			}
			var vessel *domain.Vessel
			if run.VesselID != nil {
				if found, ok := snapshot.Vessels[*run.VesselID]; ok {
					vesselValue := found
					vessel = &vesselValue
				}
			}
			reportID, idErr := generateEntityID("rpt")
			if idErr != nil {
				return domain.CompletionReport{}, domain.NewError(domain.CodeStorage, idErr.Error())
			}
			return domain.BuildCompletionReport(
				run.Clone(), formula.Clone(), vessel, reportID, generatedAt, nextRevision,
			), nil
		},
	)
	if err != nil {
		var existingReport *storage.ErrExistingReport
		if errors.As(err, &existingReport) {
			return existingReport.Existing.Clone(), true, nil
		}
		return domain.CompletionReport{}, false, err
	}
	return report.Clone(), false, nil
}

// GetCompletionReport reads and verifies an archived report by report ID.
func (s *Service) GetCompletionReport(reportID string) (domain.CompletionReport, error) {
	return s.store.GetReport(reportID)
}

// ListCompletionReports returns all report summaries, newest first.
func (s *Service) ListCompletionReports() ([]domain.ReportSummary, error) {
	summaries, err := s.store.ListReports()
	if err != nil {
		return nil, err
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].GeneratedAt.Equal(summaries[j].GeneratedAt) {
			return summaries[i].ReportID < summaries[j].ReportID
		}
		return summaries[i].GeneratedAt.After(summaries[j].GeneratedAt)
	})
	return summaries, nil
}

// ReportSummaryForRun locates the report summary reachable from a run's
// detail view. The second return value reports whether one exists.
func (s *Service) ReportSummaryForRun(runID string) (domain.ReportSummary, bool, error) {
	return s.store.SummaryByRun(runID)
}
