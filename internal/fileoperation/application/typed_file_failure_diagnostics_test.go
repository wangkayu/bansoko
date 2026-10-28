package application

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"testing"

	observabilityport "bansoko/internal/observability/port"
)

type libraryRecorder struct {
	observabilityport.Recorder
	successes []observabilityport.DiagnosticSuccessRequest
}

func (r *libraryRecorder) RecordDiagnosticSuccess(_ context.Context, req observabilityport.DiagnosticSuccessRequest) error {
	r.successes = append(r.successes, req)
	return nil
}

func TestFileFailureRequestMapsTypedErrno(t *testing.T) {
	cases := map[string]struct {
		cause   error
		kind    observabilityport.ConditionKind
		variant observabilityport.ConditionVariant
	}{
		"no space":      {&os.PathError{Op: "write", Err: syscall.ENOSPC}, observabilityport.KindLibrary, observabilityport.VariantNoSpace},
		"read only":     {&os.PathError{Op: "mkdir", Err: syscall.EROFS}, observabilityport.KindLibrary, observabilityport.VariantNotWritable},
		"permission":    {fmt.Errorf("publish: %w", fs.ErrPermission), observabilityport.KindLibrary, observabilityport.VariantNotWritable},
		"stale handle":  {&os.PathError{Op: "stat", Err: syscall.ESTALE}, observabilityport.KindLibrary, observabilityport.VariantUnreachable},
		"not connected": {&os.LinkError{Op: "rename", Err: syscall.ENOTCONN}, observabilityport.KindLibrary, observabilityport.VariantUnreachable},
		"host down":     {fmt.Errorf("x: %w", syscall.EHOSTDOWN), observabilityport.KindLibrary, observabilityport.VariantUnreachable},
		"host route":    {syscall.EHOSTUNREACH, observabilityport.KindLibrary, observabilityport.VariantUnreachable},
		"timed out":     {&os.PathError{Op: "open", Err: syscall.ETIMEDOUT}, observabilityport.KindLibrary, observabilityport.VariantUnreachable},
		"other":         {&os.PathError{Op: "open", Err: syscall.EIO}, observabilityport.KindInternal, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := fileFailureRequest(tc.cause, "op-1")
			if req.Condition.Kind != tc.kind || req.Condition.Variant != tc.variant {
				t.Fatalf("condition = %+v", req.Condition)
			}
			if tc.kind == observabilityport.KindLibrary && (req.TargetName != "" || req.AffectedID != "op-1") {
				t.Fatalf("request = %+v", req)
			}
			if !req.Validate() {
				t.Fatalf("request is not valid against the catalog: %+v", req)
			}
		})
	}
}

func TestFileSuccessClosesLibraryAndInternalSource(t *testing.T) {
	rec := &libraryRecorder{}
	s := &Service{diagnostics: rec}
	s.fileSuccessAfterCommit()(context.Background())
	if len(rec.successes) != 2 ||
		rec.successes[0] != (observabilityport.DiagnosticSuccessRequest{Kind: observabilityport.KindLibrary, TargetID: "library"}) ||
		rec.successes[1] != observabilityport.InternalSuccess(observabilityport.InternalFileOperation) {
		t.Fatalf("closes = %+v", rec.successes)
	}
}
