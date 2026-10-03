package httpapi

import "github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"

// hashForReport computes the same integrity fingerprint stored in the report
// index and sent with standalone downloads.
func hashForReport(report domain.CompletionReport) (string, error) {
	payload, err := domain.EncodeReport(report)
	if err != nil {
		return "", domain.NewError(domain.CodeStorage, "encode report: "+err.Error())
	}
	return domain.ReportHash(payload), nil
}

// marshalReportDownload returns the exact canonical bytes served as the
// standalone dossier download.
func marshalReportDownload(report domain.CompletionReport) ([]byte, error) {
	payload, err := domain.EncodeReport(report)
	if err != nil {
		return nil, domain.NewError(domain.CodeStorage, "encode report: "+err.Error())
	}
	return payload, nil
}
