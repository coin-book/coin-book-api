// Command healthcheck is the container HEALTHCHECK: distroless has no shell or curl.
package main

import (
	"context"
	"net/http"
	"os"
	"time"
)

func main() {
	client := http.Client{Timeout: 3 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8080/healthz", nil)
	if err != nil {
		os.Exit(1)
	}
	req.Host = "localhost"
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
	_ = resp.Body.Close()
}
