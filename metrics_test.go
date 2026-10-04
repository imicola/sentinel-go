package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/*
MemTotal:        8192000 kB
MemFree:            1024 kB
MemAvailable:    3072000 kB
Buffers:          128000 kB
*/

func TestMetricsHandler(t *testing.T) {
	dir := t.TempDir()
	vaildPath := filepath.Join(dir, "meminfo_vaild")
	vaildBody := []byte(
		`MemTotal:        8192000 kB
MemFree:          3072000 kB
MemAvailable:     1024    kB
Buffers:          128000  kB`)
	if err := os.WriteFile(vaildPath, vaildBody, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	brokenPath := filepath.Join(dir, "meminfo_broken")
	brokenBody := []byte(
		`MemTotal:        8192000 kB
MemFree:            1024 kB
Buffers:          128000 kB`)
	if err := os.WriteFile(brokenPath, brokenBody, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	missingPath := filepath.Join(dir, "non_existent_meminfo")

	tests := []struct {
		name          string
		method        string
		path          string
		wantStatus    int
		wantResString []string
		forbidMetric  bool
	}{
		{
			name:       "normal Test",
			method:     http.MethodGet,
			path:       vaildPath,
			wantStatus: http.StatusOK,
			wantResString: []string{
				"# HELP sentinel_memory_available_bytes",
				"# TYPE sentinel_memory_available_bytes gauge",
				"sentinel_memory_available_bytes 1048576\n",
			},
		},
		{
			name:         "borken file Test",
			method:       http.MethodGet,
			path:         brokenPath,
			wantStatus:   http.StatusInternalServerError,
			forbidMetric: true,
		},
		{
			name:         "missing file Test",
			method:       http.MethodGet,
			path:         missingPath,
			wantStatus:   http.StatusInternalServerError,
			forbidMetric: true,
		},
		{
			name:         "Method Error Test",
			method:       http.MethodPost,
			wantStatus:   http.StatusMethodNotAllowed,
			forbidMetric: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := InitMemoryStore()
			testMux := NewRouter(store, tc.path)
			req := httptest.NewRequest(tc.method, "/metrics", nil)
			rec := httptest.NewRecorder()
			testMux.ServeHTTP(rec, req)
			res := rec.Result()
			defer res.Body.Close()

			if res.StatusCode != tc.wantStatus {
				t.Errorf("expected status %d, got %d", tc.wantStatus, res.StatusCode)
			}
			body := rec.Body.String()
			// 共同
			if tc.forbidMetric && strings.Contains(body, "sentinel_memory_available_bytes") {
				t.Errorf("expected body not to contain metric, got %q", body)
			}

			// 测试1
			if tc.wantStatus == http.StatusOK {
				gotCT := res.Header.Get("Content-Type")
				if gotCT != metricsContentType {
					t.Errorf("expected Content-Type %q, got %q", metricsContentType, gotCT)
				}
				if !strings.HasSuffix(body, "\n") {
					t.Errorf("expected body to end with newline, got %q", body)
				}
				for _, want := range tc.wantResString {
					if !strings.Contains(body, want) {
						t.Errorf("expected body to contain %q, got %q", want, body)
					}
				}
			}
			// 测试 2 & 3
			if tc.wantStatus == http.StatusInternalServerError {
				if res.StatusCode != http.StatusInternalServerError {
					t.Errorf("expected status %d, got %d", http.StatusInternalServerError, res.StatusCode)
				}
			}

			if tc.wantStatus == http.StatusMethodNotAllowed {
				if res.StatusCode != http.StatusMethodNotAllowed {
					t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, res.StatusCode)
				}
				if res.Header.Get("Allow") != http.MethodGet {
					t.Errorf("expected Allow header %q, got %q", http.MethodGet, res.Header.Get("Allow"))
				}
			}
		})
	}

}
