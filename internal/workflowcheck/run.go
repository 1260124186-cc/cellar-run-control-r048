package workflowcheck

import (
	"fmt"
	"net/http/httptest"
	"os"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/httpapi"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

func Run(name string) error {
	dataDir, err := os.MkdirTemp("", "cellar-run-check-*")
	if err != nil {
		return fmt.Errorf("create check directory: %w", err)
	}
	defer os.RemoveAll(dataDir)

	var server *httptest.Server
	open := func() (*client, error) {
		store, err := storage.Open(dataDir)
		if err != nil {
			return nil, err
		}
		app := service.New(store, clock.System{})
		server = httptest.NewServer(httpapi.NewHandler(app))
		return newClient(server.URL), nil
	}
	api, err := open()
	if err != nil {
		return err
	}
	defer func() { server.Close() }()

	// restart simulates a service restart against the same data directory.
	restart := func() (*client, error) {
		server.Close()
		return open()
	}

	switch name {
	case "formula-approval":
		if err := formulaApproval(api); err != nil {
			return err
		}
	case "vessel-run-lifecycle":
		if err := vesselRunLifecycle(api); err != nil {
			return err
		}
	case "observations-completion":
		if err := observationsCompletion(api); err != nil {
			return err
		}
	case "report-archive":
		if err := reportArchive(api, restart); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown workflow check %q", name)
	}
	fmt.Printf("workflow check passed: %s\n", name)
	return nil
}
