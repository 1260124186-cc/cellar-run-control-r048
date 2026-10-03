package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

// reportResponse wraps an archived body with the integrity hash so clients
// can verify a standalone download.
type reportResponse struct {
	domain.CompletionReport
	ContentHash string `json:"content_hash"`
}

// runDetailResponse embeds the run and the summary of its archived report so
// the run detail view locates the dossier.
type runDetailResponse struct {
	domain.FermentationRun
	Report *domain.ReportSummary `json:"report,omitempty"`
}

func registerReportRoutes(mux *http.ServeMux, app *service.Service) {
	mux.HandleFunc("POST /v1/runs/{id}/reports", func(response http.ResponseWriter, request *http.Request) {
		if err := requireEmptyObjectBody(request); err != nil {
			writeDomainError(response, err)
			return
		}
		report, existing, err := app.GenerateCompletionReport(request.PathValue("id"))
		if err != nil {
			writeDomainError(response, err)
			return
		}
		hash, err := hashForReport(report)
		if err != nil {
			writeDomainError(response, err)
			return
		}
		status := http.StatusCreated
		if existing {
			// Repeat generation returns the one archived dossier; two
			// operators can never receive contradictory reports.
			status = http.StatusOK
		}
		writeJSON(response, status, reportResponse{CompletionReport: report, ContentHash: hash})
	})

	mux.HandleFunc("GET /v1/reports", func(response http.ResponseWriter, _ *http.Request) {
		summaries, err := app.ListCompletionReports()
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"reports": summaries})
	})

	mux.HandleFunc("GET /v1/reports/{id}", func(response http.ResponseWriter, request *http.Request) {
		report, err := app.GetCompletionReport(request.PathValue("id"))
		if err != nil {
			writeDomainError(response, err)
			return
		}
		hash, err := hashForReport(report)
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, reportResponse{CompletionReport: report, ContentHash: hash})
	})

	mux.HandleFunc("GET /v1/reports/{id}/download", func(response http.ResponseWriter, request *http.Request) {
		report, err := app.GetCompletionReport(request.PathValue("id"))
		if err != nil {
			writeDomainError(response, err)
			return
		}
		summary, found, err := app.ReportSummaryForRun(report.RunID)
		if err != nil {
			writeDomainError(response, err)
			return
		}
		if !found || summary.ReportID != report.ReportID {
			writeDomainError(response, domain.NewError(domain.CodeReportNotFound,
				"report was not found"))
			return
		}
		payload, err := marshalReportDownload(report)
		if err != nil {
			writeDomainError(response, err)
			return
		}
		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		response.Header().Set("Content-Disposition",
			"attachment; filename=\"report-"+report.ReportID+".json\"")
		response.Header().Set("X-Content-SHA256", summary.ContentHash)
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write(payload)
	})
}

// requireEmptyObjectBody accepts an absent body or an empty JSON object for a
// parameterless write, and rejects anything else.
func requireEmptyObjectBody(request *http.Request) error {
	if request.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBody))
	if err != nil {
		return domain.FieldError(domain.CodeInvalidInput, "body", "could not be read")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if len(raw) >= maxRequestBody {
		return domain.NewError(domain.CodeInvalidInput, "request body exceeds 1 MiB")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &body); err != nil || body == nil {
		return domain.FieldError(domain.CodeInvalidInput, "body",
			"must be an empty JSON object or omitted")
	}
	if len(body) != 0 {
		return domain.FieldError(domain.CodeInvalidInput, "body",
			"report generation takes no parameters")
	}
	return nil
}
