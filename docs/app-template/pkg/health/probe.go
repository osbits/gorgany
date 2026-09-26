package health

import (
	"net/http"
	"os"
	"time"
)

// Probe asks the running server whether it is ready: one GET to /readyz on the
// loopback interface. `app healthcheck` runs it as the container HEALTHCHECK. It
// reads only SERVER_PORT from the environment and boots nothing, so a probe every
// few seconds costs one HTTP request.
func Probe() int {
	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8080"
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/readyz") //nolint:gosec // loopback only: the port is this container's own SERVER_PORT
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
