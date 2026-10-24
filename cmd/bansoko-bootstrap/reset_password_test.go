package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseResetPasswordFlags(t *testing.T) {
	opts, err := parseResetPasswordFlags([]string{"--generate", "--username", "owner2"})
	if err != nil || !opts.generate || opts.username != "owner2" {
		t.Fatalf("opts=%+v err=%v", opts, err)
	}
	if _, err := parseResetPasswordFlags([]string{"--username"}); err == nil {
		t.Fatal("--username without a value must be rejected")
	}
	if _, err := parseResetPasswordFlags([]string{"--unknown"}); err == nil {
		t.Fatal("unknown arguments must be rejected")
	}
}

func TestGenerateResetPasswordMeetsThePasswordContract(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		password, err := generateResetPassword()
		if err != nil {
			t.Fatal(err)
		}
		if n := utf8.RuneCountInString(password); n < 12 || n > 1024 {
			t.Fatalf("generated password length %d is outside the contract", n)
		}
		if seen[password] {
			t.Fatal("generated passwords must not repeat")
		}
		seen[password] = true
	}
}

func TestCollectResetCredentialsInteractive(t *testing.T) {
	var prompts bytes.Buffer
	username, password, err := collectResetCredentials(resetPasswordOptions{},
		strings.NewReader("new-owner\nreplacement password 12\nreplacement password 12\n"), &prompts)
	if err != nil {
		t.Fatalf("interactive collection failed: %v", err)
	}
	if username != "new-owner" || password != "replacement password 12" {
		t.Fatalf("unexpected credentials: %q / %q", username, password)
	}
	if prompts.Len() == 0 {
		t.Fatal("the interactive flow must prompt")
	}
}

func TestCollectResetCredentialsRejectsMismatchedConfirmation(t *testing.T) {
	_, _, err := collectResetCredentials(resetPasswordOptions{},
		strings.NewReader("\nfirst password 12\nsecond password 12\n"), io.Discard)
	if err == nil {
		t.Fatal("mismatched confirmation must be rejected")
	}
}

func TestCollectResetCredentialsGenerateSkipsPrompts(t *testing.T) {
	var prompts bytes.Buffer
	username, password, err := collectResetCredentials(resetPasswordOptions{generate: true, username: " owner "},
		strings.NewReader(""), &prompts)
	if err != nil {
		t.Fatalf("generate collection failed: %v", err)
	}
	if username != "owner" || len(password) == 0 {
		t.Fatalf("unexpected credentials: %q / len=%d", username, len(password))
	}
	if prompts.Len() != 0 {
		t.Fatalf("--generate must never prompt, got %q", prompts.String())
	}
}

func TestDispatchRoutesResetPassword(t *testing.T) {
	var gotArgs []string
	code := dispatchWithRunners([]string{"reset-password", "--generate"}, nil, io.Discard, io.Discard,
		func(string) int { t.Fatal("server runner called"); return 0 },
		func(context.Context, []string, io.Writer, io.Writer) int { t.Fatal("recover runner called"); return 0 },
		func(_ context.Context, args []string, _ io.Reader, _, _ io.Writer) int { gotArgs = args; return 19 })
	if code != 19 || len(gotArgs) != 1 || gotArgs[0] != "--generate" {
		t.Fatalf("reset-password dispatch exit=%d args=%v", code, gotArgs)
	}
}
