package httpjson

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		wantOK   bool
		wantCode int
	}{
		{"valid", `{"a":"x"}`, true, http.StatusOK},
		{"at the cap", `{"a":"` + strings.Repeat("x", 16-8) + `"}`, true, http.StatusOK},
		{"over the cap", `{"a":"` + strings.Repeat("x", 16) + `"}`, false, http.StatusRequestEntityTooLarge},
		{"invalid json", `{"a":`, false, http.StatusBadRequest},
		{"empty", ``, false, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v struct{ A string }
			w := httptest.NewRecorder()
			ok := Decode(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body)), &v, 16)
			if ok != tc.wantOK || w.Code != tc.wantCode {
				t.Errorf("ok %v status %d, want ok %v status %d (body %q)", ok, w.Code, tc.wantOK, tc.wantCode, w.Body.String())
			}
		})
	}
}
