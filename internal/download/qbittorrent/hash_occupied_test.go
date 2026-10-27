package qbittorrent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"bansoko/internal/download/domain"
)

const (
	occupiedV1     = "0123456789abcdef0123456789abcdef01234567"
	occupiedSHA256 = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

// occupiedServer is a qB stub: tagged answers the operation-tag lookup,
// byHash answers the hash lookup (before and after the add), and add is the
// add response (status, body).
type occupiedServer struct {
	tagged    string
	byHash    []string
	addStatus int
	addBody   string

	adds        int
	hashQueries []string
}

func (s *occupiedServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if hashes := r.URL.Query().Get("hashes"); hashes != "" {
				s.hashQueries = append(s.hashQueries, hashes)
				answer := "[]"
				if len(s.byHash) > 0 {
					answer = s.byHash[0]
					if len(s.byHash) > 1 {
						s.byHash = s.byHash[1:]
					}
				}
				_, _ = w.Write([]byte(answer))
				return
			}
			if s.tagged == "" {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_, _ = w.Write([]byte(s.tagged))
		case "/api/v2/torrents/add":
			s.adds++
			w.WriteHeader(s.addStatus)
			_, _ = w.Write([]byte(s.addBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func prepareOccupied(t *testing.T, server *httptest.Server) (*PreparedSubmission, domain.ProviderTask, bool, error) {
	t.Helper()
	return (Client{Endpoint: server.URL}).PrepareSubmission(context.Background(), []byte("payload"), "show.torrent",
		domain.PathSnapshot{RemoteRoot: "/downloads"}, "occupied-key", []string{occupiedV1, occupiedSHA256})
}

func TestHashCandidatesCoverBothHashFormsAndTheTruncatedForm(t *testing.T) {
	got := HashCandidates(" "+strings.ToUpper(occupiedV1)+" ", occupiedV1, occupiedSHA256, "")
	want := []string{occupiedV1, occupiedSHA256, occupiedSHA256[:40]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates=%v want %v", got, want)
	}
}

func TestPrepareSubmissionRefusesHashHeldByAnotherTask(t *testing.T) {
	stub := &occupiedServer{byHash: []string{`[{"hash":"` + occupiedV1 + `","tags":"bansoko-other-key, user"}]`}, addStatus: http.StatusOK, addBody: "Ok."}
	server := stub.start(t)
	prepared, _, found, err := prepareOccupied(t, server)
	if !errors.Is(err, ErrHashOccupied) || prepared != nil || found {
		t.Fatalf("prepare prepared=%v found=%v err=%v", prepared, found, err)
	}
	if stub.adds != 0 {
		t.Fatalf("an occupied hash still sent %d add requests", stub.adds)
	}
	if len(stub.hashQueries) != 1 || stub.hashQueries[0] != occupiedV1+"|"+occupiedSHA256+"|"+occupiedSHA256[:40] {
		t.Fatalf("hash queries=%v", stub.hashQueries)
	}
	if got := classifyProviderError(err); got != domain.ProviderErrorTerminal {
		t.Fatalf("occupied classification=%v", got)
	}
}

func TestPrepareSubmissionAdoptsOwnTagBeforeHashCheck(t *testing.T) {
	stub := &occupiedServer{
		tagged:    `[{"added_on":2,"hash":"` + occupiedV1 + `","name":"Show","progress":0.5,"state":"downloading","tags":"bansoko-occupied-key"}]`,
		byHash:    []string{`[{"hash":"` + occupiedV1 + `","tags":"bansoko-occupied-key"}]`},
		addStatus: http.StatusOK, addBody: "Ok.",
	}
	server := stub.start(t)
	prepared, task, found, err := prepareOccupied(t, server)
	if err != nil || !found || prepared != nil || task.ID != "tag:bansoko-occupied-key" {
		t.Fatalf("own tag must be adopted: prepared=%v task=%+v found=%v err=%v", prepared, task, found, err)
	}
	if stub.adds != 0 || len(stub.hashQueries) != 0 {
		t.Fatalf("own-tag adoption sent adds=%d hash queries=%v", stub.adds, stub.hashQueries)
	}
}

func TestPrepareSubmissionWithoutHashesSkipsHashCheck(t *testing.T) {
	stub := &occupiedServer{byHash: []string{`[{"hash":"` + occupiedV1 + `","tags":"other"}]`}, addStatus: http.StatusOK, addBody: "Ok."}
	server := stub.start(t)
	prepared, _, found, err := (Client{Endpoint: server.URL}).PrepareSubmission(context.Background(), []byte("payload"), "show.torrent",
		domain.PathSnapshot{RemoteRoot: "/downloads"}, "occupied-key", nil)
	if err != nil || found || prepared == nil || len(stub.hashQueries) != 0 {
		t.Fatalf("no hashes must skip the check: prepared=%v found=%v err=%v queries=%v", prepared, found, err, stub.hashQueries)
	}
}

func TestPreparedSubmissionAttributesRefusedAddToOccupyingTask(t *testing.T) {
	other := `[{"hash":"` + occupiedV1 + `","tags":"someone-else"}]`
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"qB 4.x Fails.", http.StatusOK, "Fails."},
		{"qB 5.x 409", http.StatusConflict, "Torrent is already present"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// The pre-check sees nothing (the other task appears in between);
			// the re-read after the refusal finds it.
			stub := &occupiedServer{byHash: []string{"[]", other}, addStatus: test.status, addBody: test.body}
			server := stub.start(t)
			prepared, _, _, err := prepareOccupied(t, server)
			if err != nil || prepared == nil {
				t.Fatalf("prepare err=%v", err)
			}
			result, err := prepared.Execute(context.Background())
			if !errors.Is(err, ErrHashOccupied) || result.Accepted || !result.ResponseReceived || result.StatusCode != test.status {
				t.Fatalf("refused add result=%+v err=%v", result, err)
			}
			if stub.adds != 1 || len(stub.hashQueries) != 2 {
				t.Fatalf("adds=%d hash queries=%v", stub.adds, stub.hashQueries)
			}
		})
	}
}

func TestPreparedSubmissionRefusalWithoutOccupyingTaskStaysOrdinary(t *testing.T) {
	for _, after := range []string{"[]", `[{"hash":"` + occupiedV1 + `","tags":"bansoko-occupied-key"}]`} {
		stub := &occupiedServer{byHash: []string{"[]", after}, addStatus: http.StatusOK, addBody: "Fails."}
		server := stub.start(t)
		prepared, _, _, err := prepareOccupied(t, server)
		if err != nil || prepared == nil {
			t.Fatalf("prepare err=%v", err)
		}
		result, err := prepared.Execute(context.Background())
		if err == nil || errors.Is(err, ErrHashOccupied) || result.Accepted {
			t.Fatalf("refusal with re-read %s must stay an ordinary failure: result=%+v err=%v", after, result, err)
		}
	}
}

func TestPreparedSubmissionOnlyAttributesQBRefusalShapes(t *testing.T) {
	other := `[{"hash":"` + occupiedV1 + `","tags":"someone-else"}]`
	for _, status := range []int{http.StatusInternalServerError, http.StatusUnsupportedMediaType} {
		stub := &occupiedServer{byHash: []string{"[]", other}, addStatus: status, addBody: "error"}
		server := stub.start(t)
		prepared, _, _, err := prepareOccupied(t, server)
		if err != nil || prepared == nil {
			t.Fatalf("prepare err=%v", err)
		}
		if _, err := prepared.Execute(context.Background()); err == nil || errors.Is(err, ErrHashOccupied) {
			t.Fatalf("HTTP %d is not a duplicate refusal: err=%v", status, err)
		}
		if len(stub.hashQueries) != 1 {
			t.Fatalf("HTTP %d re-read the hashes: %v", status, stub.hashQueries)
		}
	}
}
