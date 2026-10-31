package api

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestWriteJSONSetsLength(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]int{"a": 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("Content-Length %q, body %d bytes", got, rec.Body.Len())
	}
}

func TestWriteJSONEncodeErrorIs500(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]float64{"cpu": math.NaN()})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if rec.Body.String() != "{\"error\":\"internal\"}\n" {
		t.Fatalf("body %q", rec.Body.String())
	}
}
