package main

import (
	"reflect"
	"testing"
)

func TestRuntimeCommand(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		mode string
		rest []string
	}{
		{name: "default serve", mode: "serve"},
		{name: "explicit serve", args: []string{"serve", "--listen", "127.0.0.1:0"}, mode: "serve", rest: []string{"--listen", "127.0.0.1:0"}},
		{name: "serve flags stay unchanged", args: []string{"--listen", "127.0.0.1:0"}, mode: "serve", rest: []string{"--listen", "127.0.0.1:0"}},
		{name: "bootstrap", args: []string{"bootstrap", "recover", "status"}, mode: "bootstrap", rest: []string{"recover", "status"}},
		{name: "migrate", args: []string{"migrate", "rotate-secret-key"}, mode: "migrate", rest: []string{"rotate-secret-key"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mode, rest := runtimeCommand(test.args)
			if mode != test.mode || !reflect.DeepEqual(rest, test.rest) {
				t.Fatalf("runtimeCommand(%v) = %q, %v; want %q, %v", test.args, mode, rest, test.mode, test.rest)
			}
		})
	}
}
