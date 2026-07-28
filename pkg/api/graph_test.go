package api

import (
	"encoding/json"
	"testing"
)

func TestGraphRepositorySelectorJSON(t *testing.T) {
	for _, test := range []struct {
		input string
		id    int64
		name  string
		valid bool
	}{
		{`101`, 101, "", true},
		{`"owner/repo"`, 0, "owner/repo", true},
		{`0`, 0, "", false},
		{`-1`, 0, "", false},
		{`1.0`, 0, "", false},
		{`""`, 0, "", false},
		{`{"id":101}`, 0, "", false},
		{`101 102`, 0, "", false},
	} {
		var got GraphRepositorySelector
		err := json.Unmarshal([]byte(test.input), &got)
		if (err == nil) != test.valid || got.ID != test.id || got.Name != test.name {
			t.Fatalf("%s => %#v, %v", test.input, got, err)
		}
	}
}

func TestGraphRepositorySelectorOmittedFromRequests(t *testing.T) {
	data, err := json.Marshal(GraphCypherRequest{Statement: "RETURN 1"})
	if err != nil || string(data) != `{"statement":"RETURN 1"}` {
		t.Fatalf("JSON = %s, %v", data, err)
	}
}

func TestGraphContractsMarshalBoundedFields(t *testing.T) {
	response := GraphContextResponse{
		Status: "found",
		Symbol: &GraphSymbol{UID: "symbol:a", Name: "A", FilePath: "a.go"},
		Incoming: map[string][]GraphReference{
			"calls": {{SourceUID: "symbol:b", TargetUID: "symbol:a"}},
		},
		Commits: map[string]string{"acme/one": "abc"},
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"status":"found","symbol":{"uid":"symbol:a","name":"A","kind":"","file_path":"a.go","language":"","repository_id":0,"range":{"start_line":0,"start_character":0,"end_line":0,"end_character":0},"test":false},"incoming":{"calls":[{"source_repository_id":0,"target_repository_id":0,"source_uid":"symbol:b","target_uid":"symbol:a","kind":"","range":{"start_line":0,"start_character":0,"end_line":0,"end_character":0},"confidence":0}]},"commits":{"acme/one":"abc"}}`
	if string(data) != want {
		t.Fatalf("JSON = %s", data)
	}
}
