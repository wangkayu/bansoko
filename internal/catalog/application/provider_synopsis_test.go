package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"bansoko/internal/catalog/domain"
)

type synopsisTestReader struct {
	calls  int
	result domain.EntrySynopsis
	err    error
}

func (r *synopsisTestReader) ReadSeason(context.Context, string, domain.ReadHints) (domain.ReadResult, error) {
	return domain.ReadResult{}, nil
}

func (r *synopsisTestReader) ReadSynopsis(context.Context, string) (domain.EntrySynopsis, error) {
	r.calls++
	return r.result, r.err
}

func newSynopsisTestService(reader *synopsisTestReader) *Service {
	service := NewProviderService(nil, map[string]Reader{ProviderMikan: reader})
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	return service
}

func TestProviderEntrySynopsisCachesOneEntry(t *testing.T) {
	value := "简介"
	reader := &synopsisTestReader{result: domain.EntrySynopsis{Provider: ProviderMikan, ProviderEntryRef: "301", Synopsis: &value}}
	service := newSynopsisTestService(reader)

	first, err := service.ProviderEntrySynopsis(context.Background(), "mikan", "301")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ProviderEntrySynopsis(context.Background(), " MIKAN ", " 301 ")
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 {
		t.Fatalf("reader calls = %d, want 1", reader.calls)
	}
	if first.Synopsis == nil || second.Synopsis == nil || *first.Synopsis != *second.Synopsis {
		t.Fatalf("cached answers differ: %+v / %+v", first, second)
	}
	if _, err := service.ProviderEntrySynopsis(context.Background(), "mikan", "4057"); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 2 {
		t.Fatalf("a different entry must not reuse the cache, calls = %d", reader.calls)
	}
}

func TestProviderEntrySynopsisRejectsInvalidReferences(t *testing.T) {
	reader := &synopsisTestReader{}
	service := newSynopsisTestService(reader)
	for _, ref := range []string{"", "0", "01", "-1", "abc"} {
		_, err := service.ProviderEntrySynopsis(context.Background(), "mikan", ref)
		if !errors.Is(err, ErrSynopsisEntryInvalid) {
			t.Fatalf("ref %q err = %v, want ErrSynopsisEntryInvalid", ref, err)
		}
	}
	if _, err := service.ProviderEntrySynopsis(context.Background(), "nyaa", "301"); !errors.Is(err, ErrSynopsisEntryInvalid) {
		t.Fatalf("non-catalog provider err = %v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("invalid references must not reach the provider, calls = %d", reader.calls)
	}
}

func TestProviderEntrySynopsisDoesNotCacheFailures(t *testing.T) {
	reader := &synopsisTestReader{err: ErrProviderUpstream}
	service := newSynopsisTestService(reader)
	for range 2 {
		if _, err := service.ProviderEntrySynopsis(context.Background(), "mikan", "301"); !errors.Is(err, ErrProviderUpstream) {
			t.Fatalf("err = %v, want ErrProviderUpstream", err)
		}
	}
	if reader.calls != 2 {
		t.Fatalf("reader calls = %d, want 2", reader.calls)
	}
}

func TestProviderEntrySynopsisRequiresAReader(t *testing.T) {
	service := NewProviderService(nil, nil)
	if _, err := service.ProviderEntrySynopsis(context.Background(), "mikan", "301"); err == nil {
		t.Fatal("a missing reader must fail")
	}
}
