package application

import (
	"errors"
	"fmt"
	"testing"

	"bansoko/internal/apperrors"
)

func TestMapQBErrorSeparatesUnreachableFromRejectedLogin(t *testing.T) {
	refused := fmt.Errorf("login to qB: %w", errors.New(`Post "http://127.0.0.1:1/api/v2/auth/login": dial tcp 127.0.0.1:1: connect: connection refused`))
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"login transport failure", refused, "QB_UNREACHABLE"},
		{"login rejected", errors.New("qB login failed with HTTP 403"), "QB_AUTH_FAILED"},
		{"request failure", fmt.Errorf("request qB torrent list: %w", errors.New("i/o timeout")), "QB_UNREACHABLE"},
		{"other", errors.New("decode qB torrent list: unexpected EOF"), "QB_REQUEST_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var badRequest *apperrors.BadRequest
			if err := mapQBError(tc.err); !errors.As(err, &badRequest) || badRequest.Code != tc.code {
				t.Fatalf("mapQBError(%v) = %v, want %s", tc.err, err, tc.code)
			}
		})
	}
}
