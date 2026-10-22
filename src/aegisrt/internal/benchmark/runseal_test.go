package benchmark

import (
	"reflect"
	"testing"
)

func TestValidateArtifactPayloadsRejectsUnsafeReservedAndDuplicateNames(t *testing.T) {
	tests := []struct {
		name     string
		payloads []artifactPayload
	}{
		{name: "parent traversal", payloads: []artifactPayload{{Name: "../scheduler.csv", Kind: "scheduler-csv", MediaType: "text/csv", Records: 1, Data: []byte("x")}}},
		{name: "forward slash", payloads: []artifactPayload{{Name: "nested/scheduler.csv", Kind: "scheduler-csv", MediaType: "text/csv", Records: 1, Data: []byte("x")}}},
		{name: "backslash", payloads: []artifactPayload{{Name: `nested\scheduler.csv`, Kind: "scheduler-csv", MediaType: "text/csv", Records: 1, Data: []byte("x")}}},
		{name: "manifest", payloads: []artifactPayload{{Name: "manifest.json", Kind: "environment-json", MediaType: "application/json", Records: 1, Data: []byte("{}")}}},
		{name: "checksum", payloads: []artifactPayload{{Name: "SHA256SUMS", Kind: "environment-json", MediaType: "application/json", Records: 1, Data: []byte("{}")}}},
		{name: "case insensitive duplicate", payloads: []artifactPayload{
			{Name: "result.csv", Kind: "scheduler-csv", MediaType: "text/csv", Records: 1, Data: []byte("a")},
			{Name: "RESULT.csv", Kind: "scheduler-csv", MediaType: "text/csv", Records: 1, Data: []byte("b")},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateArtifactPayloads(test.payloads); err == nil {
				t.Fatal("unsafe artifact payloads were accepted")
			}
		})
	}
}

func TestChecksumNamesAreDerivedFromArtifactPayloads(t *testing.T) {
	payloads := []artifactPayload{
		{Name: "scheduler.csv", Kind: "scheduler-csv", MediaType: "text/csv", Records: 1, Data: []byte("a")},
		{Name: "environment.json", Kind: "environment-json", MediaType: "application/json", Records: 1, Data: []byte("{}")},
	}
	want := []string{"scheduler.csv", "environment.json", "manifest.json"}
	if got := checksumNames(payloads); !reflect.DeepEqual(got, want) {
		t.Fatalf("checksum names = %#v, want %#v", got, want)
	}
}
