// Package outbound classifies the error of an outbound network call into a
// small closed set of classes. It is a pure leaf: standard library only, no
// persistence or observability dependencies, so any module that dials out
// (notification delivery, metadata providers, update source, downloader, feeds)
// can share one vocabulary for "what kind of failure was this".
package outbound

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"syscall"
)

// Class is the failure class of an outbound call. The set is closed.
type Class string

const (
	ClassTimeout           Class = "timeout"
	ClassConnectionRefused Class = "connection_refused"
	ClassConnectionReset   Class = "connection_reset"
	ClassDNS               Class = "dns"
	ClassTLS               Class = "tls"
	ClassUnknown           Class = "unknown"
)

// Classify maps err to its class by error type and sentinel only. It never
// reads err.Error(): error text of a network call can carry URLs, credentials
// and upstream response text, so nothing derived from it may reach a
// diagnostic. A nil error or one no rule recognises is ClassUnknown.
func Classify(err error) Class {
	if err == nil {
		return ClassUnknown
	}
	// DNS first: a resolver timeout is still a DNS failure for the reader.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ClassDNS
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return ClassConnectionRefused
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return ClassConnectionReset
	}
	if isTLS(err) {
		return ClassTLS
	}
	if isTimeout(err) {
		return ClassTimeout
	}
	return ClassUnknown
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	return timeoutInChain(err)
}

// timeoutInChain reports whether any error in the chain is a net.Error whose
// Timeout() is true. errors.As alone is not enough: it stops at the first
// net.Error, and a wrapper such as *url.Error answers Timeout() from its own
// direct cause only, hiding a timeout one wrap deeper.
func timeoutInChain(err error) bool {
	for err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return true
		}
		switch wrapped := err.(type) {
		case interface{ Unwrap() error }:
			err = wrapped.Unwrap()
		case interface{ Unwrap() []error }:
			for _, child := range wrapped.Unwrap() {
				if timeoutInChain(child) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

// isTLS reports a handshake or certificate failure. A handshake timeout is not
// here: it is a net.Error timeout and classes as ClassTimeout.
func isTLS(err error) bool {
	var (
		verification *tls.CertificateVerificationError
		recordHeader tls.RecordHeaderError
		alert        tls.AlertError
		unknownCA    x509.UnknownAuthorityError
		hostname     x509.HostnameError
		invalid      x509.CertificateInvalidError
		constraint   x509.ConstraintViolationError
		insecure     x509.InsecureAlgorithmError
		systemRoots  x509.SystemRootsError
	)
	return errors.As(err, &verification) ||
		errors.As(err, &recordHeader) ||
		errors.As(err, &alert) ||
		errors.As(err, &unknownCA) ||
		errors.As(err, &hostname) ||
		errors.As(err, &invalid) ||
		errors.As(err, &constraint) ||
		errors.As(err, &insecure) ||
		errors.As(err, &systemRoots)
}
