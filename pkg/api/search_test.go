package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSearchResponseReportsTruncation(t *testing.T) {
	data, err := json.Marshal(SearchResponse{Truncated: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"truncated":true`) {
		t.Fatalf("response = %s", data)
	}
}
