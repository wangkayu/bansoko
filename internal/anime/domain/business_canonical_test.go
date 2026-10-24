package domain

import "testing"

// The business digest bytes equal the database and client canonical JSON:
// sorted keys, literal < > &, literal U+2028/U+2029, and an escaped backslash
// before "u2028" text stays escaped.
func TestBusinessDigestBytesMatchDatabaseCanonicalJSON(t *testing.T) {
	got, err := businessDigestBytes([]byte(`{"b":["(?P<ep>\\d+)","a&b","x y ","\\u2028"],"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"a\":1,\"b\":[\"(?P<ep>\\\\d+)\",\"a&b\",\"x y \",\"\\\\u2028\"]}"
	if string(got) != want {
		t.Fatalf("digest bytes\n got %s\nwant %s", got, want)
	}
}
