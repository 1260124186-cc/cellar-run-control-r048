package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// ReportConclusion is the terminal outcome captured by a completion report.
type ReportConclusion string

const (
	ReportConclusionCompleted ReportConclusion = "completed"
	ReportConclusionAborted   ReportConclusion = "aborted"
)

// ReportFormulaArchive is the immutable formula snapshot carried away with a
// released batch. It is copied at report generation time so later formula
// retirement or other lifecycle changes can never alter the archive.
type ReportFormulaArchive struct {
	ID                    string        `json:"id"`
	Name                  string        `json:"name"`
	Style                 string        `json:"style"`
	Version               int64         `json:"version"`
	TargetOriginalGravity float64       `json:"target_original_gravity"`
	TargetFinalGravity    float64       `json:"target_final_gravity"`
	MaxFermentationTempC  float64       `json:"max_fermentation_temp_c"`
	MinimumDays           int           `json:"minimum_days"`
	MaximumDays           int           `json:"maximum_days"`
	ApprovedAt            *time.Time    `json:"approved_at,omitempty"`
	Steps                 []FormulaStep `json:"steps"`
}

// ReportVesselArchive identifies the vessel the batch occupied.
type ReportVesselArchive struct {
	ID             string  `json:"id"`
	Code           string  `json:"code"`
	CapacityL      float64 `json:"capacity_liters"`
	PlannedVolumeL float64 `json:"planned_volume_liters"`
}

// CompletionReport is the frozen completion dossier for one run. It is never
// mutated after creation: every field is copied from the run, its formula,
// and its vessel at generation time.
type CompletionReport struct {
	ReportID         string               `json:"report_id"`
	RunID            string               `json:"run_id"`
	RunCode          string               `json:"run_code"`
	GeneratedAt      time.Time            `json:"generated_at"`
	Conclusion       ReportConclusion     `json:"conclusion"`
	SnapshotRevision int64                `json:"snapshot_revision"`
	RunState         RunState             `json:"run_state"`
	RunVersion       int64                `json:"run_version"`
	PlannedVolumeL   float64              `json:"planned_volume_liters"`
	Notes            string               `json:"notes,omitempty"`
	StartedAt        *time.Time           `json:"started_at,omitempty"`
	EndedAt          *time.Time           `json:"ended_at,omitempty"`
	Formula          ReportFormulaArchive `json:"formula"`
	Vessel           *ReportVesselArchive `json:"vessel,omitempty"`
	Observations     []GravityObservation `json:"observations"`
	Metrics          *RunMetrics          `json:"metrics,omitempty"`
}

func (r CompletionReport) Clone() CompletionReport {
	cloned := r
	cloned.Observations = append([]GravityObservation(nil), r.Observations...)
	cloned.Formula.Steps = append([]FormulaStep(nil), r.Formula.Steps...)
	if r.StartedAt != nil {
		value := *r.StartedAt
		cloned.StartedAt = &value
	}
	if r.EndedAt != nil {
		value := *r.EndedAt
		cloned.EndedAt = &value
	}
	if r.Formula.ApprovedAt != nil {
		value := *r.Formula.ApprovedAt
		cloned.Formula.ApprovedAt = &value
	}
	if r.Vessel != nil {
		value := *r.Vessel
		cloned.Vessel = &value
	}
	if r.Metrics != nil {
		value := *r.Metrics
		cloned.Metrics = &value
	}
	return cloned
}

// ReportSummary is the indexed projection of a report that lives in the
// revisioned snapshot. Every field must match the archived body; the content
// hash additionally certifies the exact archived bytes.
type ReportSummary struct {
	ReportID         string           `json:"report_id"`
	RunID            string           `json:"run_id"`
	RunCode          string           `json:"run_code"`
	Conclusion       ReportConclusion `json:"conclusion"`
	RunState         RunState         `json:"run_state"`
	GeneratedAt      time.Time        `json:"generated_at"`
	SnapshotRevision int64            `json:"snapshot_revision"`
	ContentHash      string           `json:"content_hash"`
}

// BuildCompletionReport freezes a concluded run together with the formula
// version and vessel in effect when the batch is released.
func BuildCompletionReport(
	run FermentationRun,
	formula Formula,
	vessel *Vessel,
	reportID string,
	generatedAt time.Time,
	snapshotRevision int64,
) CompletionReport {
	report := CompletionReport{
		ReportID:         reportID,
		RunID:            run.ID,
		RunCode:          run.Code,
		GeneratedAt:      generatedAt.UTC(),
		SnapshotRevision: snapshotRevision,
		RunState:         run.State,
		RunVersion:       run.Version,
		PlannedVolumeL:   run.PlannedVolumeL,
		Notes:            run.Notes,
		StartedAt:        cloneTime(run.StartedAt),
		EndedAt:          cloneTime(run.EndedAt),
		Formula: ReportFormulaArchive{
			ID:                    formula.ID,
			Name:                  formula.Name,
			Style:                 formula.Style,
			Version:               formula.Version,
			TargetOriginalGravity: formula.TargetOriginalGravity,
			TargetFinalGravity:    formula.TargetFinalGravity,
			MaxFermentationTempC:  formula.MaxFermentationTempC,
			MinimumDays:           formula.MinimumDays,
			MaximumDays:           formula.MaximumDays,
			ApprovedAt:            cloneTime(formula.ApprovedAt),
			Steps:                 append([]FormulaStep(nil), formula.Steps...),
		},
		Observations: append([]GravityObservation(nil), run.Observations...),
	}
	if run.State == RunCompleted {
		report.Conclusion = ReportConclusionCompleted
	} else {
		report.Conclusion = ReportConclusionAborted
	}
	if run.Metrics != nil {
		metrics := *run.Metrics
		report.Metrics = &metrics
	}
	if vessel != nil {
		report.Vessel = &ReportVesselArchive{
			ID:             vessel.ID,
			Code:           vessel.Code,
			CapacityL:      vessel.CapacityL,
			PlannedVolumeL: run.PlannedVolumeL,
		}
	}
	return report
}

// BuildReportSummary derives the indexed summary from a stored report body and
// the hash of its exact archived bytes.
func BuildReportSummary(report CompletionReport, contentHash string) ReportSummary {
	return ReportSummary{
		ReportID:         report.ReportID,
		RunID:            report.RunID,
		RunCode:          report.RunCode,
		Conclusion:       report.Conclusion,
		RunState:         report.RunState,
		GeneratedAt:      report.GeneratedAt,
		SnapshotRevision: report.SnapshotRevision,
		ContentHash:      contentHash,
	}
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := value.UTC()
	return &copied
}

// EncodeReport returns the canonical archived bytes of a report body. The
// same encoding is used for the on-disk body, the integrity hash, and the
// standalone download endpoint.
func EncodeReport(report CompletionReport) ([]byte, error) {
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}

// ReportHash is the integrity fingerprint of canonical report bytes.
func ReportHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}
