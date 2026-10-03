package httpapi

import (
	"fmt"
	"net/http"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

func registerReportRoutes(mux *http.ServeMux, app *service.Service) {
	mux.HandleFunc("POST /v1/runs/{id}/report", func(response http.ResponseWriter, request *http.Request) {
		report, created, err := app.GenerateRunReport(request.PathValue("id"))
		if err != nil {
			writeDomainError(response, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(response, status, report)
	})

	mux.HandleFunc("GET /v1/reports", func(response http.ResponseWriter, _ *http.Request) {
		summaries, err := app.ListReports()
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"reports": summaries})
	})

	mux.HandleFunc("GET /v1/reports/{number}", func(response http.ResponseWriter, request *http.Request) {
		report, err := app.GetReport(request.PathValue("number"))
		if err != nil {
			writeDomainError(response, err)
			return
		}
		response.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", report.Number+".json"))
		writeJSON(response, http.StatusOK, report)
	})
}
