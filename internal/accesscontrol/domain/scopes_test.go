package domain

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeApiTokenScopes(t *testing.T) {
	valid := map[string][]string{
		"":                   {ScopeRead, ScopeWrite},
		"read":               {ScopeRead},
		"write,read":         {ScopeRead, ScopeWrite},
		"secrets,read":       {ScopeRead, ScopeSecrets},
		"secrets,write,read": {ScopeRead, ScopeWrite, ScopeSecrets},
	}
	for input, want := range valid {
		var requested []string
		if input != "" {
			requested = splitScopes(input)
		}
		got, err := NormalizeApiTokenScopes(requested)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: got %v %v, want %v", input, got, err, want)
		}
		if decoded := DecodeScopes(EncodeScopes(got)); !reflect.DeepEqual(decoded, want) {
			t.Fatalf("%q: stored value does not round-trip: %v", input, decoded)
		}
	}
	for _, invalid := range [][]string{{ScopeWrite}, {ScopeRead, ScopeRead}, {ScopeRead, "admin"}, {""}} {
		if _, err := NormalizeApiTokenScopes(invalid); !errors.Is(err, ErrApiTokenInvalid) {
			t.Fatalf("%v must be refused, got %v", invalid, err)
		}
	}
	for _, stored := range []string{"", "write", "read,admin", "write,read"} {
		if decoded := DecodeScopes(stored); decoded != nil {
			t.Fatalf("a non-canonical stored value %q must decode to no scope, got %v", stored, decoded)
		}
	}
	account := Account{Scopes: []string{ScopeRead}}
	if !account.HasScope(ScopeRead) || account.HasScope(ScopeWrite) || account.HasScopes([]string{ScopeRead, ScopeSecrets}) {
		t.Fatal("HasScope must follow the held set")
	}
}

func splitScopes(value string) []string {
	var out []string
	start := 0
	for index := 0; index <= len(value); index++ {
		if index == len(value) || value[index] == ',' {
			out = append(out, value[start:index])
			start = index + 1
		}
	}
	return out
}
