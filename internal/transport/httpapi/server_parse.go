package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/transport/httpapi/generated"
)

// TitleParserService is the side-effect-free release title parse boundary. It
// calls the exact parser version the evaluation path uses.
type TitleParserService interface {
	ParseTitle(ctx context.Context, title string) (generated.ParseReleaseTitleResult, error)
}

// TorrentParseService is the side-effect-free torrent fetch + file-list parse
// boundary. It fetches and bencode-validates with the shared SSRF-safe
// transport and size limits; no torrent input row or Release is created.
type TorrentParseService interface {
	ParseTorrentURL(ctx context.Context, torrentURL string) (generated.ParseTorrentFilesResult, error)
}

// ParseReleaseTitle parses one release title with the evaluation parser
// version. Pure function; no product write.
func (s *Server) ParseReleaseTitle(w http.ResponseWriter, r *http.Request) {
	var input generated.ParseReleaseTitleInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := parseReleaseTitlePure(r.Context(), input.Title)
	if err != nil {
		writeError(w, r, unprocessable("TITLE_PARSE_FAILED", "The release title failed to parse.", nil))
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, result)
}

// ParseTorrentFiles fetches and parses one torrent URL's file list. Side-effect
// free: no owner, WorkItem, torrent input row, or Release is created.
func (s *Server) ParseTorrentFiles(w http.ResponseWriter, r *http.Request) {
	var input generated.ParseTorrentFilesInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := s.deps.TorrentParse.ParseTorrentURL(r.Context(), input.TorrentUrl)
	if err != nil {
		var invalid *acquisitionInvalidError
		if errors.As(err, &invalid) {
			writeError(w, r, unprocessable("TORRENT_PARSE_INVALID", invalid.Error(), nil))
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, result)
}

// acquisitionInvalidError marks bounded validation/parse failures that belong
// at 422 rather than the generic 500 mapping.
type acquisitionInvalidError struct{ message string }

func (e *acquisitionInvalidError) Error() string { return e.message }

// parseReleaseTitlePure mirrors the evaluation path parser: ParseReleaseFacts
// (release-facts-v1) plus the default episode pattern list, returning the same
// parser version constants the preview/execution path persists.
func parseReleaseTitlePure(ctx context.Context, title string) (generated.ParseReleaseTitleResult, error) {
	_ = ctx
	rules, _, err := attrrules.Current()
	if err != nil {
		return generated.ParseReleaseTitleResult{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return generated.ParseReleaseTitleResult{}, errors.New("release title is required")
	}
	facts := acquisitiondomain.ParseReleaseFacts(title, rules)
	parsed := generated.ParsedReleaseFacts{}
	if facts.GroupName != "" {
		parsed.GroupName = &facts.GroupName
	}
	if facts.Quality != "" {
		parsed.Quality = &facts.Quality
	}
	if facts.Language != "" {
		parsed.Languages = &[]string{facts.Language}
	}
	special := facts.Special
	parsed.Special = &special
	result := generated.ParseReleaseTitleResult{
		Facts:         parsed,
		ParserVersion: animedomain.ParserVersion,
	}
	for _, pattern := range acquisitiondomain.DefaultEpisodePatterns {
		episode, err := acquisitiondomain.ParseEpisode(title, pattern, 0)
		if err == nil {
			episodeNumber := float32(episode.EpisodeNumber)
			result.Episode = &generated.EpisodeParseResult{
				Matched:       true,
				EpisodeNumber: episodeNumber,
				Confidence:    &episode.Confidence,
				Evidence:      &episode.Evidence,
			}
			break
		}
	}
	return result, nil
}

// torrentParseService is the wire-ready default implementation: it fetches
// with the shared SSRF-safe torrent reader and never persists anything.
type torrentParseService struct {
	reader acquisitionapplication.TorrentPayloadReader
}

func (s *torrentParseService) ParseTorrentURL(ctx context.Context, torrentURL string) (generated.ParseTorrentFilesResult, error) {
	payload, err := s.reader.FetchPayload(ctx, torrentURL)
	if err != nil {
		return generated.ParseTorrentFilesResult{}, &acquisitionInvalidError{message: err.Error()}
	}
	digest := "sha256:" + sha256Hex(payload.Bytes)
	files := make([]generated.ParseTorrentFile, 0, len(payload.Metadata.Files))
	for _, file := range payload.Metadata.Files {
		entry := generated.ParseTorrentFile{Path: file.Path, Name: file.Name, SizeBytes: file.SizeBytes}
		if file.MediaKind != "" {
			entry.MediaKind = &file.MediaKind
		}
		if file.Language != "" {
			entry.Language = &file.Language
		}
		files = append(files, entry)
	}
	infoHashV2 := payload.Metadata.InfoHashV2
	return generated.ParseTorrentFilesResult{
		InfoHashV1:     payload.Metadata.InfoHashV1,
		InfoHashV2:     stringPtr(infoHashV2),
		TorrentName:    payload.Metadata.TorrentName,
		TorrentVersion: payload.Metadata.TorrentVersion,
		TotalSizeBytes: payload.Metadata.TotalSize,
		Files:          files,
		Digest:         digest,
		ParserVersion:  rssapplication.TorrentInputParserVersion,
	}, nil
}

// NewTorrentParseService builds the wire-ready torrent parse service with the
// shared SSRF-safe reader. AllowPrivateHosts stays empty (default deny).
func NewTorrentParseService() TorrentParseService {
	return &torrentParseService{reader: acquisitionapplication.HTTPTorrentMetadataReader{}}
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	v := value
	return &v
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
