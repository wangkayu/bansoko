package adapter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAniBTCompletenessAndActiveEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"bgmId":123,"total":2,"groups":[{"slug":"active-id","name":"Same","status":"active"},{"slug":"inactive-id","name":"Same","status":"disabled"}]}}`))
	}))
	defer server.Close()
	got, err := (AniBTGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroups(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || got.CompletenessEvidence == "" || len(got.Groups) != 2 || got.Groups[0].Active == nil || !*got.Groups[0].Active || got.Groups[1].Active == nil || *got.Groups[1].Active {
		t.Fatalf("incomplete evidence: %+v", got)
	}
}
func TestAniBTUnknownStatusCannotAuthorizeRetirement(t *testing.T) {
	for _, status := range []string{"", "  ", "UNRECOGNIZED"} {
		t.Run(fmt.Sprintf("%q", status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"ok":true,"data":{"bgmId":123,"total":1,"groups":[{"slug":"id","name":"Name","status":%q}]}}`, status)
			}))
			defer server.Close()
			got, err := (AniBTGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroups(t.Context(), "123")
			if err != nil || got.Complete || len(got.Groups) != 1 || got.Groups[0].Active != nil {
				t.Fatalf("insufficient status evidence: %+v, %v", got, err)
			}
		})
	}
}
