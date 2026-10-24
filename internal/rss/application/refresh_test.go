package application

import (
	"encoding/json"
	"testing"
)

func TestRefreshTriggerIsManual(t *testing.T) {
	tests := []struct {
		name     string
		snapshot json.RawMessage
		want     bool
	}{
		{name: "manual trigger", snapshot: json.RawMessage(`{"trigger":"manual"}`), want: true},
		{name: "subscribe initial trigger", snapshot: json.RawMessage(`{"trigger":"subscribe_initial"}`), want: false},
		{name: "dispatcher item carries no snapshot", snapshot: nil, want: false},
		{name: "corrupt snapshot", snapshot: json.RawMessage(`{broken`), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := refreshTriggerIsManual(test.snapshot); got != test.want {
				t.Fatalf("refreshTriggerIsManual(%s) = %v, want %v", test.snapshot, got, test.want)
			}
		})
	}
}
