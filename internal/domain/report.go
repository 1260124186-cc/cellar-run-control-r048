package domain

import "time"

type ReportConclusion string

const (
	ConclusionCompleted ReportConclusion = "completed"
	ConclusionAborted   ReportConclusion = "aborted"
)

// ReportFormulaSnapshot freezes the formula as it was when the report was
// generated. Later formula changes never rewrite an archived report.
type ReportFormulaSnapshot struct {
	ID                    string        `json:"id"`
	Name                  string        `json:"name"`
	Style                 string        `json:"style"`
	TargetOriginalGravity float64       `json:"target_original_gravity"`
	TargetFinalGravity    float64       `json:"target_final_gravity"`
	MaxFermentationTempC  float64       `json:"max_fermentation_temp_c"`
	MinimumDays           int           `json:"minimum_days"`
	MaximumDays           int           `json:"maximum_days"`
	Steps                 []FormulaStep `json:"steps"`
	Version               int64         `json:"version"`
	ApprovedAt            *time.Time    `json:"approved_at,omitempty"`
}

// ReportVesselSnapshot freezes the vessel identity used by the run.
type ReportVesselSnapshot struct {
	ID        string  `json:"id"`
	Code      string  `json:"code"`
	CapacityL float64 `json:"capacity_liters"`
}

// CompletionReport is the immutable release archive for a finished run. It is
// created once per run and kept under its report number.
type CompletionReport struct {
	Number         string                `json:"number"`
	RunID          string                `json:"run_id"`
	RunCode        string                `json:"run_code"`
	Conclusion     ReportConclusion      `json:"conclusion"`
	Formula        ReportFormulaSnapshot `json:"formula"`
	Vessel         *ReportVesselSnapshot `json:"vessel,omitempty"`
	PlannedVolumeL float64               `json:"planned_volume_liters"`
	Notes          string                `json:"notes,omitempty"`
	StartedAt      *time.Time            `json:"started_at,omitempty"`
	EndedAt        *time.Time            `json:"ended_at,omitempty"`
	Observations   []GravityObservation  `json:"observations"`
	Metrics        *RunMetrics           `json:"metrics,omitempty"`
	GeneratedAt    time.Time             `json:"generated_at"`
}

// ReportSummary is the index-level view of an archived report.
type ReportSummary struct {
	Number      string           `json:"number"`
	RunID       string           `json:"run_id"`
	RunCode     string           `json:"run_code"`
	Conclusion  ReportConclusion `json:"conclusion"`
	GeneratedAt time.Time        `json:"generated_at"`
}

func NewCompletionReport(
	number string,
	run FermentationRun,
	formula Formula,
	vessel *Vessel,
	now time.Time,
) (CompletionReport, error) {
	var conclusion ReportConclusion
	switch run.State {
	case RunCompleted:
		conclusion = ConclusionCompleted
	case RunAborted:
		conclusion = ConclusionAborted
	default:
		return CompletionReport{}, NewError(CodeRunState,
			"only a completed or aborted run can be archived")
	}
	report := CompletionReport{
		Number:         number,
		RunID:          run.ID,
		RunCode:        run.Code,
		Conclusion:     conclusion,
		Formula:        snapshotFormula(formula),
		PlannedVolumeL: run.PlannedVolumeL,
		Notes:          run.Notes,
		Observations:   append([]GravityObservation{}, run.Observations...),
		GeneratedAt:    now.UTC(),
	}
	if run.StartedAt != nil {
		value := *run.StartedAt
		report.StartedAt = &value
	}
	if run.EndedAt != nil {
		value := *run.EndedAt
		report.EndedAt = &value
	}
	if run.Metrics != nil {
		value := *run.Metrics
		report.Metrics = &value
	}
	if vessel != nil {
		report.Vessel = &ReportVesselSnapshot{
			ID:        vessel.ID,
			Code:      vessel.Code,
			CapacityL: vessel.CapacityL,
		}
	}
	return report, nil
}

func snapshotFormula(formula Formula) ReportFormulaSnapshot {
	snapshot := ReportFormulaSnapshot{
		ID:                    formula.ID,
		Name:                  formula.Name,
		Style:                 formula.Style,
		TargetOriginalGravity: formula.TargetOriginalGravity,
		TargetFinalGravity:    formula.TargetFinalGravity,
		MaxFermentationTempC:  formula.MaxFermentationTempC,
		MinimumDays:           formula.MinimumDays,
		MaximumDays:           formula.MaximumDays,
		Steps:                 append([]FormulaStep{}, formula.Steps...),
		Version:               formula.Version,
	}
	if formula.ApprovedAt != nil {
		value := *formula.ApprovedAt
		snapshot.ApprovedAt = &value
	}
	return snapshot
}

func (r CompletionReport) Clone() CompletionReport {
	cloned := r
	cloned.Formula.Steps = append([]FormulaStep(nil), r.Formula.Steps...)
	if r.Formula.ApprovedAt != nil {
		value := *r.Formula.ApprovedAt
		cloned.Formula.ApprovedAt = &value
	}
	if r.Vessel != nil {
		value := *r.Vessel
		cloned.Vessel = &value
	}
	if r.StartedAt != nil {
		value := *r.StartedAt
		cloned.StartedAt = &value
	}
	if r.EndedAt != nil {
		value := *r.EndedAt
		cloned.EndedAt = &value
	}
	cloned.Observations = append([]GravityObservation(nil), r.Observations...)
	if r.Metrics != nil {
		value := *r.Metrics
		cloned.Metrics = &value
	}
	return cloned
}

func (r CompletionReport) Summary() ReportSummary {
	return ReportSummary{
		Number:      r.Number,
		RunID:       r.RunID,
		RunCode:     r.RunCode,
		Conclusion:  r.Conclusion,
		GeneratedAt: r.GeneratedAt,
	}
}
