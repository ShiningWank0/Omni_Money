package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONResponseSerializationFailureIsSafe500(t *testing.T) {
	w := httptest.NewRecorder()
	jsonResponse(w, make(chan int), http.StatusOK)
	if w.Code != 500 || !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), "chan") {
		t.Fatal("serialization error leaked or succeeded")
	}
}
