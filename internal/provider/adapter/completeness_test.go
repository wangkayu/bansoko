package adapter

import (
	"context"
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
func TestAniBTMissingStatusCannotAuthorizeRetirement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"data":{"bgmId":123,"total":1,"groups":[{"slug":"id","name":"Name"}]}}`))
	}))
	defer server.Close()
	got, err := (AniBTGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroups(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Complete {
		t.Fatal("unknown status claimed complete")
	}
}
