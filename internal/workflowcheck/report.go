package workflowcheck

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

func reportArchive(api *client, restart func() (*client, error)) error {
	formula, err := ensureApprovedFormula(api, "Report Archive Formula")
	if err != nil {
		return err
	}
	vessel, err := createCheckVessel(api, "VSL-301", 300)
	if err != nil {
		return err
	}
	started, err := createStartedRun(api, formula.ID, vessel.ID, "RUN-REPORT-MAIN")
	if err != nil {
		return err
	}

	base := time.Now().UTC().Add(10 * time.Second)
	gravities := []float64{1.050, 1.028, 1.014}
	version := started.Run.Version
	for index, gravity := range gravities {
		sequence := index + 1
		response, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/observations",
			map[string]any{
				"sequence":      sequence,
				"gravity":       gravity,
				"temperature_c": 20.0,
				"observed_at":   verifiedAt(base, sequence),
			}, ptrInt64(version))
		if err != nil {
			return err
		}
		if err := requireStatus(response, 200, "append report observation"); err != nil {
			return err
		}
		var run runView
		if err := response.decode(&run); err != nil {
			return err
		}
		version = run.Version
	}
	conditioning, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/conditioning",
		nil, ptrInt64(version))
	if err != nil {
		return err
	}
	if err := requireStatus(conditioning, 200, "condition report run"); err != nil {
		return err
	}
	var conditioningRun runView
	if err := conditioning.decode(&conditioningRun); err != nil {
		return err
	}
	completed, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/complete",
		nil, ptrInt64(conditioningRun.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(completed, 200, "complete report run"); err != nil {
		return err
	}
	var completedPair pairView
	if err := completed.decode(&completedPair); err != nil {
		return err
	}
	if completedPair.Run.Metrics == nil {
		return errInvalidWorkflow("completed run has no metrics to archive")
	}

	// An open run cannot be archived.
	openRun, err := api.call("POST", "/v1/runs",
		createRunPayload("RUN-REPORT-OPEN", formula.ID), nil)
	if err != nil {
		return err
	}
	if err := requireStatus(openRun, 201, "create open run"); err != nil {
		return err
	}
	var open runView
	if err := openRun.decode(&open); err != nil {
		return err
	}
	premature, err := api.call("POST", "/v1/runs/"+open.ID+"/report", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(premature, 409, string(domain.CodeRunState),
		"archive an open run"); err != nil {
		return err
	}

	// Two operators archive the same run at the same time. Exactly one
	// generation may be created; both responses must carry identical content.
	var first, second response
	var firstErr, secondErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		first, firstErr = api.call("POST", "/v1/runs/"+started.Run.ID+"/report", nil, nil)
	}()
	go func() {
		defer wg.Done()
		second, secondErr = api.call("POST", "/v1/runs/"+started.Run.ID+"/report", nil, nil)
	}()
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if secondErr != nil {
		return secondErr
	}
	statuses := map[int]int{first.status: 1, second.status: 1}
	if statuses[201] != 1 || statuses[200] != 1 {
		return fmt.Errorf("concurrent generation: expected one 201 and one 200, got %d and %d",
			first.status, second.status)
	}
	if string(first.body) != string(second.body) {
		return errInvalidWorkflow("concurrent generation produced contradictory reports")
	}
	var report domain.CompletionReport
	if err := first.decode(&report); err != nil {
		return err
	}
	if err := requireReportContent(report, started.Run.ID, formula, vessel); err != nil {
		return err
	}
	if report.Metrics == nil || *report.Metrics != *completedPair.Run.Metrics {
		return errInvalidWorkflow("report did not freeze the final metrics")
	}

	// Repeating generation returns the same archive.
	repeat, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/report", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(repeat, 200, "regenerate existing report"); err != nil {
		return err
	}
	if string(repeat.body) != string(first.body) {
		return errInvalidWorkflow("repeated generation changed the archived report")
	}

	// An aborted run archives with an aborted conclusion and no metrics.
	abortVessel, err := createCheckVessel(api, "VSL-302", 300)
	if err != nil {
		return err
	}
	abortRun, err := api.call("POST", "/v1/runs",
		createRunPayload("RUN-REPORT-ABORT", formula.ID), nil)
	if err != nil {
		return err
	}
	if err := requireStatus(abortRun, 201, "create abort run"); err != nil {
		return err
	}
	var aborting runView
	if err := abortRun.decode(&aborting); err != nil {
		return err
	}
	reserved, err := api.call("POST", "/v1/runs/"+aborting.ID+"/reserve",
		map[string]any{"vessel_id": abortVessel.ID}, ptrInt64(aborting.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(reserved, 200, "reserve abort vessel"); err != nil {
		return err
	}
	var reservedPair pairView
	if err := reserved.decode(&reservedPair); err != nil {
		return err
	}
	aborted, err := api.call("POST", "/v1/runs/"+aborting.ID+"/abort",
		nil, ptrInt64(reservedPair.Run.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(aborted, 200, "abort reserved run"); err != nil {
		return err
	}
	abortReportResponse, err := api.call("POST", "/v1/runs/"+aborting.ID+"/report", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(abortReportResponse, 201, "archive aborted run"); err != nil {
		return err
	}
	var abortReport domain.CompletionReport
	if err := abortReportResponse.decode(&abortReport); err != nil {
		return err
	}
	if abortReport.Number != "RPT-000002" ||
		abortReport.Conclusion != domain.ConclusionAborted ||
		abortReport.Metrics != nil ||
		len(abortReport.Observations) != 0 ||
		abortReport.Vessel == nil || abortReport.Vessel.Code != "VSL-302" {
		return errInvalidWorkflow("aborted run report has unexpected content")
	}

	// The run detail locates the archive by report number.
	detail, err := api.call("GET", "/v1/runs/"+started.Run.ID, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(detail, 200, "read archived run"); err != nil {
		return err
	}
	var detailView runView
	if err := detail.decode(&detailView); err != nil {
		return err
	}
	if detailView.ReportNumber != report.Number {
		return errInvalidWorkflow("run detail does not reference its report number")
	}

	// Summaries match the archived bodies.
	listed, err := api.call("GET", "/v1/reports", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(listed, 200, "list reports"); err != nil {
		return err
	}
	var summaries struct {
		Reports []domain.ReportSummary `json:"reports"`
	}
	if err := listed.decode(&summaries); err != nil {
		return err
	}
	if len(summaries.Reports) != 2 ||
		summaries.Reports[0].Number != report.Number ||
		summaries.Reports[1].Number != abortReport.Number {
		return errInvalidWorkflow("report summaries do not match the archived reports")
	}
	if summaries.Reports[0].RunID != report.RunID ||
		summaries.Reports[0].Conclusion != report.Conclusion ||
		!summaries.Reports[0].GeneratedAt.Equal(report.GeneratedAt) {
		return errInvalidWorkflow("report summary does not match its body")
	}

	// The archive downloads individually by report number.
	missing, err := api.call("GET", "/v1/reports/RPT-999999", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(missing, 404, string(domain.CodeReportNotFound),
		"download unknown report"); err != nil {
		return err
	}
	download, err := api.call("GET", "/v1/reports/"+report.Number, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(download, 200, "download report"); err != nil {
		return err
	}
	if disposition := download.headers.Get("Content-Disposition"); !strings.Contains(
		disposition, "attachment") || !strings.Contains(disposition, report.Number) {
		return errInvalidWorkflow("report download is missing its attachment header")
	}
	if string(download.body) != string(first.body) {
		return errInvalidWorkflow("downloaded report differs from the generated report")
	}

	// Later formula changes must not rewrite the archived formula version.
	abortOpen, err := api.call("POST", "/v1/runs/"+open.ID+"/abort", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(abortOpen, 200, "abort open run"); err != nil {
		return err
	}
	retired, err := api.call("POST", "/v1/formulas/"+formula.ID+"/retire",
		nil, ptrInt64(formula.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(retired, 200, "retire archived formula"); err != nil {
		return err
	}
	afterRetire, err := api.call("GET", "/v1/reports/"+report.Number, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(afterRetire, 200, "download report after formula retire"); err != nil {
		return err
	}
	var frozen domain.CompletionReport
	if err := afterRetire.decode(&frozen); err != nil {
		return err
	}
	if frozen.Formula.Version != formula.Version {
		return errInvalidWorkflow("archived formula version changed after retirement")
	}

	// After a restart, summaries and bodies must still line up and generation
	// must still converge on the existing archive.
	restarted, err := restart()
	if err != nil {
		return err
	}
	api = restarted
	restartedDownload, err := api.call("GET", "/v1/reports/"+report.Number, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(restartedDownload, 200, "download report after restart"); err != nil {
		return err
	}
	if string(restartedDownload.body) != string(download.body) {
		return errInvalidWorkflow("report body changed across a restart")
	}
	restartedDetail, err := api.call("GET", "/v1/runs/"+started.Run.ID, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(restartedDetail, 200, "read run after restart"); err != nil {
		return err
	}
	var restartedView runView
	if err := restartedDetail.decode(&restartedView); err != nil {
		return err
	}
	if restartedView.ReportNumber != report.Number {
		return errInvalidWorkflow("run detail lost its report number across a restart")
	}
	restartedList, err := api.call("GET", "/v1/reports", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(restartedList, 200, "list reports after restart"); err != nil {
		return err
	}
	if restartedList.body == nil || string(restartedList.body) != string(listed.body) {
		return errInvalidWorkflow("report summaries changed across a restart")
	}
	regenerate, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/report", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(regenerate, 200, "regenerate report after restart"); err != nil {
		return err
	}
	if string(regenerate.body) != string(first.body) {
		return errInvalidWorkflow("restart produced a conflicting report for the same run")
	}
	return nil
}

func requireReportContent(
	report domain.CompletionReport,
	runID string,
	formula formulaView,
	vessel vesselView,
) error {
	if report.Number != "RPT-000001" {
		return fmt.Errorf("expected report number RPT-000001, got %q", report.Number)
	}
	if report.RunID != runID || report.RunCode != "RUN-REPORT-MAIN" {
		return errInvalidWorkflow("report does not reference its run")
	}
	if report.Conclusion != domain.ConclusionCompleted {
		return errInvalidWorkflow("completed run report has the wrong conclusion")
	}
	if report.Formula.ID != formula.ID ||
		report.Formula.Version != formula.Version ||
		report.Formula.Name != "Report Archive Formula" ||
		len(report.Formula.Steps) != 3 {
		return errInvalidWorkflow("report did not freeze the formula version")
	}
	if report.Vessel == nil || report.Vessel.ID != vessel.ID || report.Vessel.Code != "VSL-301" {
		return errInvalidWorkflow("report did not freeze the vessel")
	}
	if len(report.Observations) != 3 || report.Observations[0].Gravity != 1.050 {
		return errInvalidWorkflow("report did not freeze the observation curve")
	}
	if report.GeneratedAt.IsZero() {
		return errInvalidWorkflow("report has no generation timestamp")
	}
	return nil
}
