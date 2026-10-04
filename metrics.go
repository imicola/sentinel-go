package main

import (
	"fmt"
	"net/http"
)

const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

func NewMetricsHandler(meminfoPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		value, err := readMemAvailable(meminfoPath)
		if err != nil {
			http.Error(w, "memory collection failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", metricsContentType)
		body := fmt.Sprintf(
			"# HELP sentinel_memory_available_bytes Available memory in bytes.\n"+
				"# TYPE sentinel_memory_available_bytes gauge\n"+
				"sentinel_memory_available_bytes %d\n", value)
		if _, err := fmt.Fprint(w, body); err != nil {
			return
		}
	}
}
