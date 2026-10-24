package application

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bansoko/internal/download/admission"
	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
)

type admissionProvider struct {
	operationTask  domain.ProviderTask
	operationFound bool
	operationErr   error
	hashTask       domain.ProviderTask
	hashFound      bool
	hashErr        error
	calls          []string
}

func (p *admissionProvider) LookupByOperationKey(context.Context, string) (domain.ProviderTask, bool, error) {
	return domain.ProviderTask{}, false, nil
}
func (p *admissionProvider) LookupByOperationKeyWithSnapshot(_ context.Context, key string, _ domain.PathSnapshot) (domain.ProviderTask, bool, error) {
	p.calls = append(p.calls, "operation:"+key)
	return p.operationTask, p.operationFound, p.operationErr
}
func (p *admissionProvider) LookupByInfoHashesWithSnapshot(_ context.Context, hashes []string, _ domain.PathSnapshot) (domain.ProviderTask, bool, error) {
	p.calls = append(p.calls, "hash:"+strings.Join(hashes, ","))
	return p.hashTask, p.hashFound, p.hashErr
}
func (*admissionProvider) Sync(context.Context, string) (domain.ProviderTask, error) {
	return domain.ProviderTask{}, nil
}
func (*admissionProvider) ClassifyProviderError(error) domain.ProviderErrorClass {
	return domain.ProviderErrorAmbiguous
}

func admissionService(provider Provider) *Service {
	return &Service{provider: provider, runtimeConfig: &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{
		SaveRoot: "/downloads", ApplicationRoot: "/srv/downloads",
	}}}
}

// admissionTask carries the real qB-reported evidence shape: the frozen
// PathSnapshot echo is intentionally absent from the cross-owner facts.
func admissionTask(hash string) domain.ProviderTask {
	return domain.ProviderTask{ID: "hash:" + hash, Hash: hash, SavePath: "/downloads",
		Path: "/downloads/show/01.mkv", Paths: []string{"/downloads/show/01.mkv"}, ContentPath: "/downloads/show"}
}

func TestMatchAdmissionChecksOperationKeyBeforeHash(t *testing.T) {
	hash := strings.Repeat("a", 40)
	replayTask := admissionTask(hash)
	replaySnapshot := domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/srv/downloads"}
	// B-local replay: the provider echoes the frozen snapshot, as the real client does.
	replayTask.PathSnapshot = &replaySnapshot
	provider := &admissionProvider{operationTask: replayTask, operationFound: true}
	result, err := admissionService(provider).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
	if err != nil || result.Provenance != nil || result.PathSnapshot.RemoteRoot != "/downloads" {
		t.Fatalf("operation-key reentry = %+v, err=%v", result, err)
	}
	if got := strings.Join(provider.calls, "|"); got != "operation:qb:release-b" {
		t.Fatalf("lookup order = %q, want operation only", got)
	}
}

func TestMatchAdmissionFallsBackToUniqueFrozenHashAndPath(t *testing.T) {
	hash := strings.Repeat("b", 40)
	provider := &admissionProvider{hashTask: admissionTask(hash), hashFound: true}
	result, err := admissionService(provider).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
	if err != nil || result.Provenance == nil || result.Provenance.Kind != "unique_infohash_path" {
		t.Fatalf("match = %+v, err=%v", result, err)
	}
	if got := strings.Join(provider.calls, "|"); got != "operation:qb:release-b|hash:"+hash {
		t.Fatalf("lookup order = %q", got)
	}
}

// TestMatchAdmissionAdoptsFromContentPathOnly proves a qB-reported
// torrents/info ContentPath inside the frozen root is sufficient real
// evidence even when the files listing is empty.
func TestMatchAdmissionAdoptsFromContentPathOnly(t *testing.T) {
	hash := strings.Repeat("c", 40)
	provider := &admissionProvider{hashTask: domain.ProviderTask{ID: "hash:" + hash, Hash: hash, SavePath: "/downloads", ContentPath: "/downloads/show"}, hashFound: true}
	result, err := admissionService(provider).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
	if err != nil || result.Provenance == nil || result.Provenance.Kind != "unique_infohash_path" {
		t.Fatalf("content-path adoption = %+v, err=%v", result, err)
	}
}

// TestMatchAdmissionAdoptsContentPathUnderApplicationRoot proves the frozen
// application root is a legal evidence root under a mapped deployment, not only
// the qB-visible remote root.
func TestMatchAdmissionAdoptsContentPathUnderApplicationRoot(t *testing.T) {
	hash := strings.Repeat("ac", 20)
	provider := &admissionProvider{hashTask: domain.ProviderTask{
		ID: "hash:" + hash, Hash: hash, SavePath: "/downloads", ContentPath: "/srv/downloads/show",
	}, hashFound: true}
	result, err := admissionService(provider).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
	if err != nil || result.Provenance == nil || result.Provenance.Kind != "unique_infohash_path" {
		t.Fatalf("application-root adoption = %+v, err=%v", result, err)
	}
}

// TestMatchAdmissionSnapshotEchoCannotSelfCertifyAdoption proves the client's
// own frozen PathSnapshot echo is never evidence for the cross-owner hash
// fallback: a task that echoes the frozen snapshot but reports no content path
// has no real provider evidence and must fail closed.
func TestMatchAdmissionSnapshotEchoCannotSelfCertifyAdoption(t *testing.T) {
	hash := strings.Repeat("ad", 20)
	echo := domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/srv/downloads"}
	task := domain.ProviderTask{ID: "hash:" + hash, Hash: hash, SavePath: "/downloads", PathSnapshot: &echo}
	provider := &admissionProvider{hashTask: task, hashFound: true}
	_, err := admissionService(provider).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
	if !errors.Is(err, admission.ErrConflictingMatch) {
		t.Fatalf("snapshot echo without content evidence = %v, want typed conflict", err)
	}
}

func TestMatchAdmissionZeroMatchKeepsFrozenPathForNormalSubmit(t *testing.T) {
	provider := &admissionProvider{}
	result, err := admissionService(provider).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: strings.Repeat("c", 40)})
	if err != nil || result.Provenance != nil || result.PathSnapshot.RemoteRoot != "/downloads" {
		t.Fatalf("zero match = %+v, err=%v", result, err)
	}
}

func TestMatchAdmissionFailsClosedOnAmbiguousOrUnsafeMatch(t *testing.T) {
	hash := strings.Repeat("d", 40)
	for name, provider := range map[string]*admissionProvider{
		"ambiguous":       {hashErr: qbittorrent.ErrLookupAmbiguous},
		"wrong hash":      {hashTask: admissionTask(strings.Repeat("e", 40)), hashFound: true},
		"wrong save root": {hashTask: func() domain.ProviderTask { task := admissionTask(hash); task.SavePath = "/other"; return task }(), hashFound: true},
		"outside application root": {hashTask: func() domain.ProviderTask {
			task := admissionTask(hash)
			task.Path = "/tmp/01.mkv"
			task.Paths = nil
			return task
		}(), hashFound: true},
		"content path outside root": {hashTask: func() domain.ProviderTask {
			task := admissionTask(hash)
			task.ContentPath = "/tmp/show"
			return task
		}(), hashFound: true},
		"no content evidence": {hashTask: func() domain.ProviderTask {
			task := admissionTask(hash)
			task.Path = ""
			task.Paths = nil
			task.ContentPath = ""
			return task
		}(), hashFound: true},
		"content path conflicts with files": {hashTask: func() domain.ProviderTask {
			task := admissionTask(hash)
			task.ContentPath = "/downloads/other-show"
			return task
		}(), hashFound: true},
		"traversal segment in file path": {hashTask: func() domain.ProviderTask {
			task := admissionTask(hash)
			task.Path = "/downloads/show/../escape.mkv"
			task.Paths = []string{"/downloads/show/../escape.mkv"}
			return task
		}(), hashFound: true},
		"relative file path": {hashTask: func() domain.ProviderTask {
			task := admissionTask(hash)
			task.Path = "show/01.mkv"
			task.Paths = []string{"show/01.mkv"}
			return task
		}(), hashFound: true},
		"traversal segment in content path": {hashTask: func() domain.ProviderTask {
			task := admissionTask(hash)
			task.ContentPath = "/downloads/show/../../etc"
			return task
		}(), hashFound: true},
		"relative content path": {hashTask: func() domain.ProviderTask {
			task := admissionTask(hash)
			task.ContentPath = "show/01.mkv"
			return task
		}(), hashFound: true},
		"control characters in content path": {hashTask: func() domain.ProviderTask {
			task := admissionTask(hash)
			task.ContentPath = "/downloads/show\n01.mkv"
			return task
		}(), hashFound: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := admissionService(provider).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
			if err == nil || (!errors.Is(err, admission.ErrAmbiguousMatch) && !errors.Is(err, admission.ErrConflictingMatch)) {
				t.Fatalf("error = %v, want typed fail closed", err)
			}
		})
	}
}

// TestMatchAdmissionPropagatesProviderReadErrors proves a transient qB
// files/read outage stays a visible retryable error instead of being
// misclassified as an adoption conflict.
func TestMatchAdmissionPropagatesProviderReadErrors(t *testing.T) {
	hash := strings.Repeat("f", 40)
	for name, lookupErr := range map[string]error{
		"files outage":      errors.New("read qB torrent content evidence: qB torrent files failed with HTTP 503"),
		"malformed payload": errors.New("read qB torrent content evidence: decode qB torrent files: invalid character"),
		"lookup outage":     errors.New("look up qB torrent hashes: request qB task lookup: connection reset"),
	} {
		t.Run(name, func(t *testing.T) {
			provider := &admissionProvider{hashErr: lookupErr}
			_, err := admissionService(provider).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
			if err == nil {
				t.Fatal("provider read error was swallowed")
			}
			if errors.Is(err, admission.ErrAmbiguousMatch) || errors.Is(err, admission.ErrConflictingMatch) {
				t.Fatalf("provider read error misclassified as typed match outcome: %v", err)
			}
		})
	}
}

// TestMatchAdmissionEvidenceThroughRealClient wires the production qB client
// against a fake qB so the propagation and fail-closed rules are exercised
// end to end through MatchAdmission.
func TestMatchAdmissionEvidenceThroughRealClient(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	newFakeQB := func(t *testing.T, filesStatus int, infoPayload, filesPayload string) *qbittorrent.Client {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v2/auth/login":
				_, _ = w.Write([]byte("Ok."))
			case "/api/v2/torrents/info":
				if strings.HasPrefix(r.URL.RawQuery, "tag=") {
					_, _ = w.Write([]byte(`[]`))
					return
				}
				_, _ = w.Write([]byte(infoPayload))
			case "/api/v2/torrents/files":
				if filesStatus != http.StatusOK {
					http.Error(w, "files unavailable", filesStatus)
					return
				}
				_, _ = w.Write([]byte(filesPayload))
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		return &qbittorrent.Client{Endpoint: server.URL, SaveRoot: "/downloads", ApplicationRoot: "/downloads"}
	}
	info := `[{"content_path":"/downloads/show","hash":"` + hash + `","name":"show","progress":1,"save_path":"/downloads","state":"pausedup"}]`

	t.Run("files 500 stays a visible retry", func(t *testing.T) {
		client := newFakeQB(t, http.StatusInternalServerError, info, `[]`)
		_, err := admissionService(client).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
		if err == nil || errors.Is(err, admission.ErrConflictingMatch) || errors.Is(err, admission.ErrAmbiguousMatch) {
			t.Fatalf("files 500 through real client = %v, want propagated retryable error", err)
		}
	})

	t.Run("valid content evidence adopts", func(t *testing.T) {
		client := newFakeQB(t, http.StatusOK, info, `[{"name":"show/01.mkv","progress":1,"size":42}]`)
		result, err := admissionService(client).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
		if err != nil || result.Provenance == nil || result.Provenance.Kind != "unique_infohash_path" {
			t.Fatalf("real client adoption = %+v, err=%v", result, err)
		}
	})

	t.Run("malformed files payload stays a visible retry", func(t *testing.T) {
		client := newFakeQB(t, http.StatusOK, info, `not-json`)
		_, err := admissionService(client).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
		if err == nil || errors.Is(err, admission.ErrConflictingMatch) || errors.Is(err, admission.ErrAmbiguousMatch) {
			t.Fatalf("malformed files through real client = %v, want propagated retryable error", err)
		}
	})

	// The files listing alone is real qB evidence when qB reports absolute file
	// paths: a task whose torrents/info carries no content_path still adopts when
	// every reported file path sits in the frozen roots.
	t.Run("files-only absolute evidence adopts", func(t *testing.T) {
		withoutContentPath := `[{"hash":"` + hash + `","name":"show","progress":1,"save_path":"/downloads","state":"pausedup"}]`
		client := newFakeQB(t, http.StatusOK, withoutContentPath, `[{"name":"/downloads/show/01.mkv","progress":1,"size":42}]`)
		result, err := admissionService(client).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
		if err != nil || result.Provenance == nil || result.Provenance.Kind != "unique_infohash_path" {
			t.Fatalf("files-only adoption = %+v, err=%v", result, err)
		}
	})

	// Without a content_path the client cannot resolve qB's relative file names
	// into absolute paths, so the same listing stays unresolved and admission
	// refuses rather than adopting on a self-invented path.
	t.Run("relative files without content path fails closed", func(t *testing.T) {
		withoutContentPath := `[{"hash":"` + hash + `","name":"show","progress":1,"save_path":"/downloads","state":"pausedup"}]`
		client := newFakeQB(t, http.StatusOK, withoutContentPath, `[{"name":"show/01.mkv","progress":1,"size":42}]`)
		_, err := admissionService(client).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
		if !errors.Is(err, admission.ErrConflictingMatch) {
			t.Fatalf("unresolvable relative files = %v, want typed conflict", err)
		}
	})

	t.Run("empty files list and no content path fails closed", func(t *testing.T) {
		withoutContentPath := `[{"hash":"` + hash + `","name":"show","progress":1,"save_path":"/downloads","state":"pausedup"}]`
		client := newFakeQB(t, http.StatusOK, withoutContentPath, `[]`)
		_, err := admissionService(client).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
		if !errors.Is(err, admission.ErrConflictingMatch) {
			t.Fatalf("empty evidence = %v, want typed conflict", err)
		}
	})

	t.Run("content path conflict with files fails closed", func(t *testing.T) {
		conflicting := `[{"content_path":"/downloads/other","hash":"` + hash + `","name":"other","progress":1,"save_path":"/downloads","state":"pausedup"}]`
		client := newFakeQB(t, http.StatusOK, conflicting, `[{"name":"/downloads/show/01.mkv","progress":1,"size":42}]`)
		_, err := admissionService(client).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
		if !errors.Is(err, admission.ErrConflictingMatch) {
			t.Fatalf("content/files conflict = %v, want typed conflict", err)
		}
	})

	t.Run("no content evidence fails closed", func(t *testing.T) {
		noEvidence := `[{"hash":"` + hash + `","name":"show","progress":1,"save_path":"/downloads","state":"pausedup"}]`
		client := newFakeQB(t, http.StatusOK, noEvidence, `[]`)
		_, err := admissionService(client).MatchAdmission(context.Background(), admission.Request{OperationKey: "qb:release-b", InfoHashV1: hash})
		if !errors.Is(err, admission.ErrConflictingMatch) {
			t.Fatalf("missing evidence = %v, want typed conflict", err)
		}
	})
}
