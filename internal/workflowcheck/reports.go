package workflowcheck

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/httpapi"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

type reportFormulaView struct {
	ID      string               `json:"id"`
	Name    string               `json:"name"`
	Version int64                `json:"version"`
	Steps   []domain.FormulaStep `json:"steps"`
}

type reportVesselView struct {
	ID             string  `json:"id"`
	Code           string  `json:"code"`
	CapacityL      float64 `json:"capacity_liters"`
	PlannedVolumeL float64 `json:"planned_volume_liters"`
}

type reportView struct {
	ReportID         string                      `json:"report_id"`
	RunID            string                      `json:"run_id"`
	RunCode          string                      `json:"run_code"`
	Conclusion       string                      `json:"conclusion"`
	SnapshotRevision int64                       `json:"snapshot_revision"`
	Formula          reportFormulaView           `json:"formula"`
	Vessel           *reportVesselView           `json:"vessel"`
	Observations     []domain.GravityObservation `json:"observations"`
	Metrics          *domain.RunMetrics          `json:"metrics"`
	ContentHash      string                      `json:"content_hash"`
}

type reportSummaryView struct {
	ReportID    string `json:"report_id"`
	RunID       string `json:"run_id"`
	RunCode     string `json:"run_code"`
	Conclusion  string `json:"conclusion"`
	ContentHash string `json:"content_hash"`
}

type runDetailView struct {
	runView
	Report *reportSummaryView `json:"report"`
}

type reportListResponse struct {
	Reports []reportSummaryView `json:"reports"`
}

func completionReports(api *client, dataDir string) error {
	formula, err := ensureApprovedFormula(api, "Report Formula")
	if err != nil {
		return err
	}
	vessel, err := createCheckVessel(api, "VSL-303", 500)
	if err != nil {
		return err
	}
	started, err := createStartedRun(api, formula.ID, vessel.ID, "RUN-REPORTS")
	if err != nil {
		return err
	}

	base := time.Now().UTC().Add(10 * time.Second)
	points := []float64{1.055, 1.030, 1.014}
	version := started.Run.Version
	for index, gravity := range points {
		sequence := index + 1
		obsResponse, callErr := api.call("POST", "/v1/runs/"+started.Run.ID+"/observations",
			map[string]any{
				"sequence":      sequence,
				"gravity":       gravity,
				"temperature_c": 20.0,
				"observed_at":   verifiedAt(base, sequence),
			}, ptrInt64(version))
		if callErr != nil {
			return callErr
		}
		if err := requireStatus(obsResponse, 200, "append report-check observation"); err != nil {
			return err
		}
		var updated runView
		if err := obsResponse.decode(&updated); err != nil {
			return err
		}
		version = updated.Version
	}

	conditioningResponse, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/conditioning",
		nil, ptrInt64(version))
	if err != nil {
		return err
	}
	if err := requireStatus(conditioningResponse, 200, "begin report-check conditioning"); err != nil {
		return err
	}
	var conditioning runView
	if err := conditioningResponse.decode(&conditioning); err != nil {
		return err
	}
	completeResponse, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/complete",
		nil, ptrInt64(conditioning.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(completeResponse, 200, "complete report-check run"); err != nil {
		return err
	}

	// Generation on an open run is rejected with a stable state error.
	secondFormula, err := ensureApprovedFormula(api, "Report Formula Two")
	if err != nil {
		return err
	}
	openVessel, err := createCheckVessel(api, "VSL-304", 500)
	if err != nil {
		return err
	}
	openRun, err := createStartedRun(api, secondFormula.ID, openVessel.ID, "RUN-REPORTS-OPEN")
	if err != nil {
		return err
	}
	tooEarly, err := api.call("POST", "/v1/runs/"+openRun.Run.ID+"/reports",
		map[string]any{}, nil)
	if err != nil {
		return err
	}
	if err := requireCode(tooEarly, 409, string(domain.CodeRunState),
		"report for a non-terminal run"); err != nil {
		return err
	}
	unknown, err := api.call("POST", "/v1/runs/run_missing/reports", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(unknown, 404, string(domain.CodeRunNotFound),
		"report for a missing run"); err != nil {
		return err
	}
	rejectBody, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/reports",
		map[string]any{"unexpected": true}, nil)
	if err != nil {
		return err
	}
	if err := requireCode(rejectBody, 400, string(domain.CodeInvalidInput),
		"report generation with parameters"); err != nil {
		return err
	}

	// First generation creates the dossier.
	first, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/reports", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(first, 201, "generate report"); err != nil {
		return err
	}
	var report reportView
	if err := first.decode(&report); err != nil {
		return err
	}
	if report.Formula.Version != 3 || report.Formula.ID != formula.ID {
		return errInvalidWorkflow("report did not freeze the approved formula version")
	}
	if len(report.Formula.Steps) != 3 {
		return errInvalidWorkflow("report lost the formula process steps")
	}
	if report.Vessel == nil || report.Vessel.Code != "VSL-303" ||
		report.Vessel.PlannedVolumeL != 180.0 {
		return errInvalidWorkflow("report did not archive the vessel and volume")
	}
	if len(report.Observations) != 3 || report.Metrics == nil ||
		report.Conclusion != string(domain.ReportConclusionCompleted) {
		return errInvalidWorkflow("report missing observations, metrics, or conclusion")
	}
	if report.SnapshotRevision < 1 || report.ContentHash == "" ||
		!strings.HasPrefix(report.ContentHash, "sha256:") {
		return errInvalidWorkflow("report metadata is incomplete")
	}

	// Retiring the formula after the dossier is frozen must not change the
	// archived formula version: the report survives later formula lifecycle
	// changes unchanged.
	retireResponse, err := api.call("POST", "/v1/formulas/"+formula.ID+"/retire",
		nil, ptrInt64(formula.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(retireResponse, 200, "retire formula after report generation"); err != nil {
		return err
	}
	frozenResponse, err := api.call("GET", "/v1/reports/"+report.ReportID, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(frozenResponse, 200, "read report after formula retirement"); err != nil {
		return err
	}
	var frozen reportView
	if err := frozenResponse.decode(&frozen); err != nil {
		return err
	}
	if frozen.Formula.Version != 3 || frozen.ContentHash != report.ContentHash {
		return errInvalidWorkflow("archived formula version changed after retirement")
	}

	// Repeat generation returns the same dossier, even concurrently.
	second, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/reports", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(second, 200, "repeat report generation"); err != nil {
		return err
	}
	var repeated reportView
	if err := second.decode(&repeated); err != nil {
		return err
	}
	if repeated.ReportID != report.ReportID ||
		repeated.ContentHash != report.ContentHash {
		return errInvalidWorkflow("repeat generation produced a contradictory report")
	}

	// The strongest case: two operators generating the dossier for the first
	// time, simultaneously, on a second completed run, must still end with
	// exactly one report whose content is identical for both.
	raceVessel, err := createCheckVessel(api, "VSL-305", 500)
	if err != nil {
		return err
	}
	raceStarted, err := createStartedRun(api, secondFormula.ID, raceVessel.ID,
		"RUN-REPORTS-RACE")
	if err != nil {
		return err
	}
	raceVersion := raceStarted.Run.Version
	for index, gravity := range points {
		sequence := index + 1
		obsResponse, callErr := api.call("POST", "/v1/runs/"+raceStarted.Run.ID+"/observations",
			map[string]any{
				"sequence":      sequence,
				"gravity":       gravity,
				"temperature_c": 20.0,
				"observed_at":   verifiedAt(base, sequence),
			}, ptrInt64(raceVersion))
		if callErr != nil {
			return callErr
		}
		if err := requireStatus(obsResponse, 200, "append race-check observation"); err != nil {
			return err
		}
		var updated runView
		if err := obsResponse.decode(&updated); err != nil {
			return err
		}
		raceVersion = updated.Version
	}
	raceConditioning, err := api.call("POST", "/v1/runs/"+raceStarted.Run.ID+"/conditioning",
		nil, ptrInt64(raceVersion))
	if err != nil {
		return err
	}
	if err := requireStatus(raceConditioning, 200, "begin race-check conditioning"); err != nil {
		return err
	}
	var raceConditioningView runView
	if err := raceConditioning.decode(&raceConditioningView); err != nil {
		return err
	}
	raceComplete, err := api.call("POST", "/v1/runs/"+raceStarted.Run.ID+"/complete",
		nil, ptrInt64(raceConditioningView.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(raceComplete, 200, "complete race-check run"); err != nil {
		return err
	}
	raceID, raceHash, err := concurrentFirstGenerationAgree(api, raceStarted.Run.ID)
	if err != nil {
		return err
	}

	// The race winner is independently readable and consistent for everyone.
	raceRead, err := api.call("GET", "/v1/reports/"+raceID, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(raceRead, 200, "read concurrently generated report"); err != nil {
		return err
	}
	var raceReport reportView
	if err := raceRead.decode(&raceReport); err != nil {
		return err
	}
	if raceReport.ContentHash != raceHash || raceReport.RunID != raceStarted.Run.ID {
		return errInvalidWorkflow("concurrently generated report is inconsistent")
	}

	// And repeat generations on the already-archived run all agree too.
	if err := concurrentReportsAgree(api, started.Run.ID, report.ReportID, report.ContentHash); err != nil {
		return err
	}

	// The run detail view locates the archived report.
	runResponse, err := api.call("GET", "/v1/runs/"+started.Run.ID, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(runResponse, 200, "read run detail with report"); err != nil {
		return err
	}
	var detail runDetailView
	if err := runResponse.decode(&detail); err != nil {
		return err
	}
	if detail.Report == nil || detail.Report.ReportID != report.ReportID ||
		detail.Report.ContentHash != report.ContentHash {
		return errInvalidWorkflow("run detail does not locate its report")
	}

	// The report index lists the summary.
	listResponse, err := api.call("GET", "/v1/reports", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(listResponse, 200, "list reports"); err != nil {
		return err
	}
	var list reportListResponse
	if err := listResponse.decode(&list); err != nil {
		return err
	}
	if !summariesContain(list.Reports, report.ReportID, report.ContentHash) {
		return errInvalidWorkflow("report index missing generated report")
	}

	// Reading by report ID returns body and matching hash.
	readResponse, err := api.call("GET", "/v1/reports/"+report.ReportID, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(readResponse, 200, "read report by id"); err != nil {
		return err
	}
	var readBack reportView
	if err := readResponse.decode(&readBack); err != nil {
		return err
	}
	if readBack.ContentHash != report.ContentHash || readBack.Formula.ID != formula.ID ||
		readBack.Formula.Name != "Report Formula" {
		return errInvalidWorkflow("report read returned inconsistent content")
	}
	missingReport, err := api.call("GET", "/v1/reports/rpt_missing00000000", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(missingReport, 404, string(domain.CodeReportNotFound),
		"read missing report"); err != nil {
		return err
	}

	// Standalone download carries the exact archived bytes.
	body, headers, status, err := api.getRaw("/v1/reports/" + report.ReportID + "/download")
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("download report: expected status 200, got %d: %s",
			status, strings.TrimSpace(string(body)))
	}
	if !strings.Contains(headers.Get("Content-Disposition"),
		"attachment; filename=\"report-"+report.ReportID+".json\"") {
		return errInvalidWorkflow("download is not offered as an attachment")
	}
	downloadedHash := domain.ReportHash(body)
	if downloadedHash != report.ContentHash ||
		headers.Get("X-Content-SHA256") != report.ContentHash {
		return errInvalidWorkflow("downloaded bytes do not match the archived hash")
	}
	var downloaded reportView
	if err := json.Unmarshal(body, &downloaded); err != nil {
		return err
	}
	if downloaded.ReportID != report.ReportID ||
		downloaded.Formula.Version != formula.Version {
		return errInvalidWorkflow("downloaded dossier lost the frozen formula version")
	}

	// Disk write failures must never leave an index entry without a body, or
	// a body that diverges from the index. This uses a fresh completed run.
	if err := verifyReportWriteFailures(api, dataDir, secondFormula.ID); err != nil {
		return err
	}

	// An aborted run produces an abort conclusion dossier.
	abortRunID, err := createRunForAbort(api, secondFormula.ID, "RUN-REPORTS-ABORT")
	if err != nil {
		return err
	}
	abortResponse, err := api.call("POST", "/v1/runs/"+abortRunID+"/abort", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(abortResponse, 200, "abort planned run"); err != nil {
		return err
	}
	abortReportResponse, err := api.call("POST", "/v1/runs/"+abortRunID+"/reports", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(abortReportResponse, 201, "generate abort report"); err != nil {
		return err
	}
	var abortReport reportView
	if err := abortReportResponse.decode(&abortReport); err != nil {
		return err
	}
	if abortReport.Conclusion != string(domain.ReportConclusionAborted) ||
		abortReport.Metrics != nil || len(abortReport.Observations) != 0 {
		return errInvalidWorkflow("abort report has unexpected content")
	}

	// Restart the service against the same directory: summaries and bodies
	// must still agree, and the dossier must still read identically.
	if err := verifyRestartReconciliation(dataDir, report); err != nil {
		return err
	}
	return nil
}

// verifyReportWriteFailures makes the data directory read-only while a report
// is being generated and proves that no indexed-but-bodyless dossier can ever
// become visible, that the snapshot revision is unchanged, and that the
// leftover body (if the durable rename is what fails) is reclaimed on the
// next open. Permissions are always restored before returning.
func verifyReportWriteFailures(api *client, dataDir, formulaID string) error {
	vessel, err := createCheckVessel(api, "VSL-306", 500)
	if err != nil {
		return err
	}
	started, err := createStartedRun(api, formulaID, vessel.ID, "RUN-REPORTS-FAIL")
	if err != nil {
		return err
	}
	base := time.Now().UTC().Add(10 * time.Second)
	version := started.Run.Version
	gravities := []float64{1.050, 1.026, 1.012}
	for index, gravity := range gravities {
		sequence := index + 1
		obsResponse, callErr := api.call("POST", "/v1/runs/"+started.Run.ID+"/observations",
			map[string]any{
				"sequence":      sequence,
				"gravity":       gravity,
				"temperature_c": 20.0,
				"observed_at":   verifiedAt(base, sequence),
			}, ptrInt64(version))
		if callErr != nil {
			return callErr
		}
		if err := requireStatus(obsResponse, 200, "append failure-check observation"); err != nil {
			return err
		}
		var updated runView
		if err := obsResponse.decode(&updated); err != nil {
			return err
		}
		version = updated.Version
	}
	conditioningResponse, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/conditioning",
		nil, ptrInt64(version))
	if err != nil {
		return err
	}
	var conditioning runView
	if err := conditioningResponse.decode(&conditioning); err != nil {
		return err
	}
	completeResponse, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/complete",
		nil, ptrInt64(conditioning.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(completeResponse, 200, "complete failure-check run"); err != nil {
		return err
	}

	beforeRevision := readHealthRevision(api)

	// The reports subdirectory stays writable, so the report body is encoded
	// and committed; making the data directory read-only then prevents the
	// atomic rename that publishes the index snapshot.
	if err := os.Chmod(dataDir, 0o555); err != nil {
		return fmt.Errorf("make data directory read-only: %w", err)
	}
	defer func() { _ = os.Chmod(dataDir, 0o755) }()

	failed, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/reports", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(failed, 500, string(domain.CodeStorage),
		"generate report with an unwritable data directory"); err != nil {
		return err
	}

	if got := readHealthRevision(api); got != beforeRevision {
		return errInvalidWorkflow(fmt.Sprintf(
			"revision changed despite failed report write: %d -> %d",
			beforeRevision, got))
	}

	// The run detail must not point at an indexed report that has no body.
	detailResponse, err := api.call("GET", "/v1/runs/"+started.Run.ID, nil, nil)
	if err != nil {
		return err
	}
	var detail runDetailView
	if err := detailResponse.decode(&detail); err != nil {
		return err
	}
	if detail.Report != nil {
		return errInvalidWorkflow("failed report write left an indexed dossier")
	}

	// Restore write access and reopen as a restart would. Any body committed
	// before the failed index publish is an orphan and must be reclaimed, not
	// indexed.
	if err := os.Chmod(dataDir, 0o755); err != nil {
		return fmt.Errorf("restore data directory permissions: %w", err)
	}
	reopened, err := storage.Open(dataDir)
	if err != nil {
		return fmt.Errorf("reopen after failed report write: %w", err)
	}
	summaries, err := reopened.ListReports()
	if err != nil {
		return err
	}
	for _, summary := range summaries {
		if summary.RunID == started.Run.ID {
			return errInvalidWorkflow("failed report write indexed a dossier on reopen")
		}
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "reports"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			return errInvalidWorkflow("failed report write left a temporary file behind")
		}
	}
	return nil
}

func readHealthRevision(api *client) int64 {
	response, err := api.call("GET", "/healthz", nil, nil)
	if err != nil {
		return -1
	}
	var health struct {
		Revision int64 `json:"revision"`
	}
	if err := response.decode(&health); err != nil {
		return -1
	}
	return health.Revision
}

func concurrentReportsAgree(api *client, runID, expectedID, expectedHash string) error {
	const contenders = 4
	type result struct {
		status int
		report reportView
	}
	results := make([]result, contenders)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			response, err := api.call("POST", "/v1/runs/"+runID+"/reports", nil, nil)
			if err != nil {
				return
			}
			results[index] = result{status: response.status}
			_ = response.decode(&results[index].report)
		}(i)
	}
	close(start)
	wg.Wait()

	for _, result := range results {
		if result.status != http.StatusOK {
			return fmt.Errorf("repeat concurrent generation returned status %d, want 200",
				result.status)
		}
		if result.report.ReportID != expectedID || result.report.ContentHash != expectedHash {
			return errInvalidWorkflow("concurrent repeat generations produced divergent reports")
		}
	}
	return nil
}

// concurrentFirstGenerationAgree fires first-time generation from several
// goroutines against a run that has no report yet. Exactly one contender must
// create the dossier (201) and every other contender must receive that same
// dossier (200); nobody may observe a different report.
func concurrentFirstGenerationAgree(api *client, runID string) (string, string, error) {
	const contenders = 6
	type result struct {
		status int
		report reportView
	}
	results := make([]result, contenders)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			response, err := api.call("POST", "/v1/runs/"+runID+"/reports", nil, nil)
			if err != nil {
				return
			}
			results[index] = result{status: response.status}
			_ = response.decode(&results[index].report)
		}(i)
	}
	close(start)
	wg.Wait()

	created, reused := 0, 0
	var reportID, contentHash string
	for _, result := range results {
		if result.status != http.StatusCreated && result.status != http.StatusOK {
			return "", "", fmt.Errorf("concurrent first generation returned status %d",
				result.status)
		}
		if result.report.ReportID == "" || result.report.ContentHash == "" {
			return "", "", fmt.Errorf("concurrent first generation returned an empty report")
		}
		if reportID == "" {
			reportID = result.report.ReportID
			contentHash = result.report.ContentHash
		} else if result.report.ReportID != reportID ||
			result.report.ContentHash != contentHash {
			return "", "", errInvalidWorkflow(
				"concurrent first generations produced contradictory reports")
		}
		if result.status == http.StatusCreated {
			created++
		} else {
			reused++
		}
	}
	if created != 1 || reused != contenders-1 {
		return "", "", fmt.Errorf(
			"concurrent first generation created %d reports, want exactly 1", created)
	}
	return reportID, contentHash, nil
}

func summariesContain(summaries []reportSummaryView, reportID, hash string) bool {
	for _, summary := range summaries {
		if summary.ReportID == reportID && summary.ContentHash == hash {
			return true
		}
	}
	return false
}

func createRunForAbort(api *client, formulaID, code string) (string, error) {
	runResponse, err := api.call("POST", "/v1/runs",
		createRunPayload(code, formulaID), nil)
	if err != nil {
		return "", err
	}
	if err := requireStatus(runResponse, 201, "create abort check run"); err != nil {
		return "", err
	}
	var run runView
	if err := runResponse.decode(&run); err != nil {
		return "", err
	}
	return run.ID, nil
}

func verifyRestartReconciliation(dataDir string, original reportView) error {
	reopened, err := storage.Open(dataDir)
	if err != nil {
		return fmt.Errorf("reopen storage after restart: %w", err)
	}
	app := service.New(reopened, clock.System{})
	server := httptest.NewServer(httpapi.NewHandler(app))
	defer server.Close()
	restartClient := newClient(server.URL)

	readResponse, err := restartClient.call("GET", "/v1/reports/"+original.ReportID, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(readResponse, 200, "read report after restart"); err != nil {
		return err
	}
	var after reportView
	if err := readResponse.decode(&after); err != nil {
		return err
	}
	if after.ContentHash != original.ContentHash || after.ReportID != original.ReportID {
		return errInvalidWorkflow("report hash changed across restart")
	}
	listResponse, err := restartClient.call("GET", "/v1/reports", nil, nil)
	if err != nil {
		return err
	}
	var list reportListResponse
	if err := listResponse.decode(&list); err != nil {
		return err
	}
	if !summariesContain(list.Reports, original.ReportID, original.ContentHash) {
		return errInvalidWorkflow("report summary missing after restart")
	}
	runResponse, err := restartClient.call("GET", "/v1/runs/"+original.RunID, nil, nil)
	if err != nil {
		return err
	}
	var detail runDetailView
	if err := runResponse.decode(&detail); err != nil {
		return err
	}
	if detail.Report == nil || detail.Report.ReportID != original.ReportID {
		return errInvalidWorkflow("run detail lost its report link after restart")
	}

	// A tampered body that no longer hashes to the index must stop startup.
	tampered := filepath.Join(dataDir, "reports", original.ReportID+".json")
	raw, err := os.ReadFile(tampered)
	if err != nil {
		return err
	}
	altered := strings.Replace(string(raw), `"gravity": 1.055`, `"gravity": 1.056`, 1)
	if altered == string(raw) {
		return errInvalidWorkflow("could not locate tamper target in report body")
	}
	if err := os.WriteFile(tampered, []byte(altered), 0o644); err != nil {
		return err
	}
	if _, err := storage.Open(dataDir); err == nil {
		return errInvalidWorkflow("service started despite a hash mismatch between report index and body")
	}
	// Restore the archive so deferred cleanup of the data directory is moot,
	// and confirm the good state opens again.
	if err := os.WriteFile(tampered, raw, 0o644); err != nil {
		return err
	}
	if _, err := storage.Open(dataDir); err != nil {
		return fmt.Errorf("storage failed to reopen after restoring report: %w", err)
	}

	// Orphan bodies (body committed, index never published) are reclaimed.
	orphanID := "rpt_0123456789abcdef0123"
	orphanPath := filepath.Join(dataDir, "reports", orphanID+".json")
	if err := os.WriteFile(orphanPath, []byte(`{}`), 0o644); err != nil {
		return err
	}
	finalStore, err := storage.Open(dataDir)
	if err != nil {
		return fmt.Errorf("reopen with orphan body: %w", err)
	}
	summaries, err := finalStore.ListReports()
	if err != nil {
		return err
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].ReportID < summaries[j].ReportID
	})
	for _, summary := range summaries {
		if summary.ReportID == orphanID {
			return errInvalidWorkflow("orphan report body was indexed on startup")
		}
	}
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		return errInvalidWorkflow("orphan report body was not reclaimed on startup")
	}
	return nil
}
