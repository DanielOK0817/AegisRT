package benchmark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFaultKernelAPIClientReadsContentFreeOutputSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/tasks/target-task/output-summary" ||
			request.Header.Get("Authorization") != "Bearer "+faultKernelTestToken {
			t.Errorf("unexpected request path=%q", request.URL.Path)
		}
		writeFaultKernelAPITestJSON(writer, http.StatusOK, map[string]int{
			"record_count": 1, "artifact_count": 1,
		})
	}))
	defer server.Close()
	client, err := newFaultKernelAPIClient(strings.TrimPrefix(server.URL, "http://"), []byte(faultKernelTestToken))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	count, err := client.OutputArtifactCount(context.Background(), "target-task")
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestFaultKernelAPIClientRejectsInvalidOutputSummary(t *testing.T) {
	tests := []any{
		map[string]int{"record_count": 2, "artifact_count": 1},
		map[string]int{"record_count": 1, "artifact_count": 33},
		map[string]int{"record_count": 1},
		map[string]any{"record_count": 1, "artifact_count": 1, "content": "secret"},
	}
	for _, body := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(body)
		}))
		client, err := newFaultKernelAPIClient(strings.TrimPrefix(server.URL, "http://"), []byte(faultKernelTestToken))
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		_, err = client.OutputArtifactCount(context.Background(), "target-task")
		client.Close()
		server.Close()
		if err == nil {
			t.Fatalf("accepted output summary %#v", body)
		}
	}
	client, err := newFaultKernelAPIClient("127.0.0.1:1", []byte(faultKernelTestToken))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.OutputArtifactCount(context.Background(), "bad/task"); err == nil {
		t.Fatal("accepted unsafe task id")
	}
}
