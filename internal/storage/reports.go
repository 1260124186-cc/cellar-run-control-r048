package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

const reportsDirName = "reports"

// ErrExistingReport signals that a report already exists for the run of a
// requested generation. The committed report is carried in Existing.
type ErrExistingReport struct {
	Existing domain.CompletionReport
}

func (e *ErrExistingReport) Error() string {
	return "a report already exists for this run"
}

var reportIDPattern = regexp.MustCompile(`^rpt_[0-9a-f]{12,64}$`)

func validateReportID(reportID string) error {
	if !reportIDPattern.MatchString(reportID) {
		return fmt.Errorf("invalid report identifier %q", reportID)
	}
	return nil
}

func (s *Store) reportsPath() string {
	return filepath.Join(filepath.Dir(s.path), reportsDirName)
}

func (s *Store) reportFilePath(reportID string) (string, error) {
	if err := validateReportID(reportID); err != nil {
		return "", err
	}
	return filepath.Join(s.reportsPath(), reportID+".json"), nil
}

// reportHash is the integrity fingerprint of the exact archived bytes.
func reportHash(payload []byte) string {
	return domain.ReportHash(payload)
}

func encodeReport(report domain.CompletionReport) ([]byte, error) {
	return domain.EncodeReport(report)
}

// GetReport reads and verifies an archived report body. The summary in the
// snapshot must match the body's identity and content hash.
func (s *Store) GetReport(reportID string) (domain.CompletionReport, error) {
	s.mu.RLock()
	summary, indexed := s.snapshot.ReportIndex[reportID]
	s.mu.RUnlock()
	if !indexed {
		return domain.CompletionReport{}, domain.NewError(domain.CodeReportNotFound,
			"report was not found")
	}
	report, err := s.readReportFile(reportID)
	if err != nil {
		return domain.CompletionReport{}, err
	}
	if err := verifyReport(summary, report); err != nil {
		return domain.CompletionReport{}, domain.NewError(domain.CodeStorage, err.Error())
	}
	return report, nil
}

// SummaryByRun returns the indexed report summary associated with a run.
func (s *Store) SummaryByRun(runID string) (domain.ReportSummary, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, summary := range s.snapshot.ReportIndex {
		if summary.RunID == runID {
			return summary, true, nil
		}
	}
	return domain.ReportSummary{}, false, nil
}

// ListReports returns all archived report summaries in a stable order.
func (s *Store) ListReports() ([]domain.ReportSummary, error) {
	var summaries []domain.ReportSummary
	err := s.View(func(snapshot domain.Snapshot) error {
		summaries = make([]domain.ReportSummary, 0, len(snapshot.ReportIndex))
		for _, summary := range snapshot.ReportIndex {
			summaries = append(summaries, summary)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return summaries, nil
}

// GenerateReport creates the durable body and summary index for a run inside
// one serialized transaction. The body file is committed before the summary
// is published, so a snapshot can never reference a missing body. A repeat
// generation for the same run returns the already committed report via
// *ErrExistingReport without creating a second archive.
func (s *Store) GenerateReport(
	build func(snapshot domain.Snapshot, nextRevision int64) (domain.CompletionReport, error),
) (domain.CompletionReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	candidate := s.snapshot.Clone()
	if candidate.ReportIndex == nil {
		candidate.ReportIndex = map[string]domain.ReportSummary{}
	}

	// Prepare the report first: building must be validated against the
	// current snapshot before any bytes hit disk.
	prepared, err := build(candidate, s.snapshot.Revision+1)
	if err != nil {
		return domain.CompletionReport{}, err
	}
	if err := validateReportID(prepared.ReportID); err != nil {
		return domain.CompletionReport{}, domain.NewError(domain.CodeStorage, err.Error())
	}
	if _, exists := candidate.ReportIndex[prepared.ReportID]; exists {
		return domain.CompletionReport{}, domain.NewError(domain.CodeReportConflict,
			"a report with the same identifier already exists")
	}
	for _, summary := range candidate.ReportIndex {
		if summary.RunID == prepared.RunID {
			existing, readErr := s.readReportFile(summary.ReportID)
			if readErr != nil {
				return domain.CompletionReport{}, readErr
			}
			return domain.CompletionReport{}, &ErrExistingReport{Existing: existing}
		}
	}

	payload, err := encodeReport(prepared)
	if err != nil {
		return domain.CompletionReport{}, domain.NewError(domain.CodeStorage, err.Error())
	}
	path, err := s.reportFilePath(prepared.ReportID)
	if err != nil {
		return domain.CompletionReport{}, domain.NewError(domain.CodeStorage, err.Error())
	}
	// Durable body first. If this fails nothing has been indexed and there
	// is no body the index could ever point at.
	if err := writeFileAtomic(path, ".report-*.tmp", payload); err != nil {
		return domain.CompletionReport{}, domain.NewError(domain.CodeStorage, err.Error())
	}

	summary := domain.BuildReportSummary(prepared, reportHash(payload))
	candidate.ReportIndex[prepared.ReportID] = summary
	candidate.Revision = s.snapshot.Revision + 1
	candidate.UpdatedAt = s.now()
	if err := writeSnapshot(s.path, candidate); err != nil {
		// The body is harmlessly unreferenced; it is reclaimed by startup
		// reconciliation. Drop it eagerly when the failure is local.
		_ = os.Remove(path)
		return domain.CompletionReport{}, domain.NewError(domain.CodeStorage, err.Error())
	}
	s.snapshot = candidate
	return prepared, nil
}

func (s *Store) readReportFile(reportID string) (domain.CompletionReport, error) {
	path, err := s.reportFilePath(reportID)
	if err != nil {
		return domain.CompletionReport{}, domain.NewError(domain.CodeStorage, err.Error())
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return domain.CompletionReport{}, domain.NewError(domain.CodeStorage,
			"indexed report body is missing from disk")
	}
	if err != nil {
		return domain.CompletionReport{}, domain.NewError(domain.CodeStorage,
			fmt.Sprintf("read report body: %v", err))
	}
	var report domain.CompletionReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return domain.CompletionReport{}, domain.NewError(domain.CodeStorage,
			"decode report body: "+err.Error())
	}
	if report.ReportID != reportID {
		return domain.CompletionReport{}, domain.NewError(domain.CodeStorage,
			"report body does not match its report identifier")
	}
	return report, nil
}

func verifyReport(summary domain.ReportSummary, report domain.CompletionReport) error {
	if summary.ReportID != report.ReportID ||
		summary.RunID != report.RunID ||
		summary.RunCode != report.RunCode ||
		summary.Conclusion != report.Conclusion ||
		summary.RunState != report.RunState ||
		!summary.GeneratedAt.Equal(report.GeneratedAt) ||
		summary.SnapshotRevision != report.SnapshotRevision {
		return fmt.Errorf("report summary does not match its archived body")
	}
	payload, err := encodeReport(report)
	if err != nil {
		return err
	}
	if summary.ContentHash != reportHash(payload) {
		return fmt.Errorf("report content hash does not match its archived body")
	}
	return nil
}

// reconcileReports makes on-disk report bodies and the in-memory report index
// agree before serving. Orphaned bodies (left by a crash between body commit
// and snapshot commit) are removed; index entries without a matching body, or
// bodies whose summary disagrees with the content, stop startup.
func (s *Store) reconcileReports() error {
	reportsDir := s.reportsPath()
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		return fmt.Errorf("create reports directory: %w", err)
	}
	entries, err := os.ReadDir(reportsDir)
	if err != nil {
		return fmt.Errorf("read reports directory: %w", err)
	}
	onDisk := make(map[string]bool, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".tmp") {
			_ = os.Remove(filepath.Join(reportsDir, name))
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			return fmt.Errorf("unexpected file in reports directory: %s", name)
		}
		reportID := strings.TrimSuffix(name, ".json")
		if err := validateReportID(reportID); err != nil {
			return fmt.Errorf("reports directory contains %w", err)
		}
		onDisk[reportID] = true
	}

	indexed := s.snapshot.ReportIndex
	indexedIDs := make(map[string]bool, len(indexed))
	for reportID, summary := range indexed {
		if err := validateReportID(reportID); err != nil {
			return fmt.Errorf("report index contains %w", err)
		}
		if summary.ReportID != reportID {
			return fmt.Errorf("report index key %s does not match its summary", reportID)
		}
		run, exists := s.snapshot.Runs[summary.RunID]
		if !exists {
			return fmt.Errorf("report %s references a missing run", reportID)
		}
		if run.Code != summary.RunCode {
			return fmt.Errorf("report %s run code does not match the run", reportID)
		}
		if !onDisk[reportID] {
			return fmt.Errorf("indexed report %s has no archived body", reportID)
		}
		report, err := s.readReportFile(reportID)
		if err != nil {
			return err
		}
		if err := verifyReport(summary, report); err != nil {
			return fmt.Errorf("report %s: %w", reportID, err)
		}
		indexedIDs[reportID] = true
	}
	for reportID := range onDisk {
		if !indexedIDs[reportID] {
			// Crash-safe leftover: body committed, index never published.
			if err := os.Remove(filepath.Join(reportsDir, reportID+".json")); err != nil {
				return fmt.Errorf("remove orphaned report %s: %w", reportID, err)
			}
		}
	}
	return nil
}
