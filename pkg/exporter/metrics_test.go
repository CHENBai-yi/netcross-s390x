package exporter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExporterHandler(t *testing.T) {
	targets := []struct{ URL, TCP string }{
		{"https://1.1.1.1", "1.1.1.1:443"},
	}
	exp := NewExporter(targets, 30*time.Second)
	exp.collect()

	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	handler := exp.Handler()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "netcross_system_info") {
		t.Errorf("Metrics output missing netcross_system_info: %s", body)
	}
	if !strings.Contains(body, "netcross_probe_tcp_connect_seconds") {
		t.Errorf("Metrics output missing TCP connect metric: %s", body)
	}
}
