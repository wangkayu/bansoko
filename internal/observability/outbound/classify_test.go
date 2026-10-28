package outbound

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
)

const probeSecret = "bot123456:SECRET-TOKEN https://user:pass@private.example/hook"

// wrap puts the secret into the text of every layer around err, the way a
// *url.Error and fmt.Errorf chain carry URLs and credentials in practice.
func wrap(err error) error {
	return &url.Error{Op: "Post", URL: probeSecret, Err: fmt.Errorf("%s: %w", probeSecret, err)}
}

func TestClassifyByTypeAndSentinel(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want Class
	}{
		{"nil", nil, ClassUnknown},
		{"plain error", errors.New("boom"), ClassUnknown},
		{"context canceled", context.Canceled, ClassUnknown},
		{"dns not found", &net.DNSError{Err: "no such host", Name: "x.example", IsNotFound: true}, ClassDNS},
		{"dns timeout is dns", &net.DNSError{Err: "i/o timeout", Name: "x.example", IsTimeout: true}, ClassDNS},
		{"refused", &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, ClassConnectionRefused},
		{"reset", &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}, ClassConnectionReset},
		{"deadline exceeded", context.DeadlineExceeded, ClassTimeout},
		{"os deadline", os.ErrDeadlineExceeded, ClassTimeout},
		{"net timeout", &net.OpError{Op: "dial", Net: "tcp", Err: timeoutError{}}, ClassTimeout},
		{"cert verification", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, ClassTLS},
		{"unknown authority", x509.UnknownAuthorityError{}, ClassTLS},
		{"hostname mismatch", x509.HostnameError{Host: "x.example"}, ClassTLS},
		{"invalid certificate", x509.CertificateInvalidError{Reason: x509.Expired}, ClassTLS},
		{"record header", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, ClassTLS},
		{"tls alert", tls.AlertError(40), ClassTLS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Fatalf("Classify(%v) = %q, want %q", tc.err, got, tc.want)
			}
			if tc.err == nil {
				return
			}
			if got := Classify(wrap(tc.err)); got != tc.want {
				t.Fatalf("Classify(wrapped %v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// Error text is never read: text that says "connection refused" or "timeout"
// without the typed error behind it classes as unknown.
func TestClassifyIgnoresErrorText(t *testing.T) {
	for _, text := range []string{"connection refused", "i/o timeout", "no such host", "x509: certificate signed by unknown authority", "connection reset by peer"} {
		if got := Classify(errors.New(text)); got != ClassUnknown {
			t.Fatalf("Classify(%q) = %q, want unknown", text, got)
		}
	}
}

// The classifier's whole output is a Class from the closed set; a secret in
// any layer of the wrapped error's text cannot reach it.
func TestClassifyOutputNeverCarriesErrorText(t *testing.T) {
	closed := map[Class]bool{ClassTimeout: true, ClassConnectionRefused: true, ClassConnectionReset: true, ClassDNS: true, ClassTLS: true, ClassUnknown: true}
	for _, err := range []error{
		wrap(errors.New(probeSecret)),
		wrap(&net.DNSError{Err: probeSecret, Name: probeSecret}),
		wrap(&net.OpError{Op: "dial", Err: os.NewSyscallError(probeSecret, syscall.ECONNREFUSED)}),
		wrap(&tls.CertificateVerificationError{Err: x509.HostnameError{Host: probeSecret}}),
		wrap(context.DeadlineExceeded),
	} {
		got := Classify(err)
		if !closed[got] || strings.Contains(string(got), "SECRET") || strings.Contains(string(got), "private.example") {
			t.Fatalf("class %q escaped the closed set for %q", got, err.Error())
		}
	}
}
