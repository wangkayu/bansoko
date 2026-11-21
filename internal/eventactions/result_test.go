package eventactions

import (
	"io"
	"strings"
	"testing"
)

func TestResultCaptureStrict(t *testing.T) {
	good := `{"version":1,"http_status":204,"summary":"Refresh accepted"}`
	for _, input := range []string{
		``, `null`, `[]`, `{"version":1,"version":1,"summary":"retry"}`,
		`{"version":1,"summary":"a","body":"secret"}`, `{"version":1,"summary":"a","http_status":null}`,
		`{"version":1,"summary":"a","http_status":600}`, `{"version":1,"summary":"line\nbreak"}`,
		`{"version":1,"summary":null}`, `{"version":1.0,"summary":"a"}`, `{"summary":"a"}`,
		`{"version":1,"summary":"` + strings.Repeat("近", 241) + `"}`,
		good + good, good + strings.Repeat(" ", maxResultBytes), good + "\xff",
	} {
		var capture resultCapture
		if n, err := io.WriteString(&capture, input); err != nil || n != len(input) {
			t.Fatalf("capture did not drain stdout: %d %v", n, err)
		}
		if got := capture.result(); got != (scriptResult{}) {
			t.Fatalf("invalid report accepted: %q -> %+v", input, got)
		}
	}
	var capture resultCapture
	_, _ = io.WriteString(&capture, good[:10])
	_, _ = io.WriteString(&capture, good[10:]+strings.Repeat(" ", maxResultBytes-len(good)))
	if got := capture.result(); got != (scriptResult{summary: "Refresh accepted", httpStatus: 204}) {
		t.Fatalf("valid report lost: %+v", got)
	}
}
