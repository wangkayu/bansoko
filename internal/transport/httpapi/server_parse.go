package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	acquisitionapplication "bansoko/internal/acquisition/application"
	"bansoko/internal/parser"
	"bansoko/internal/parser/reltitletags"
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

// parseReleaseTitlePure exposes the native reltitle tag/value projection and
// the same parser's episode evidence. No second attribute parser fills legacy
// group, quality, language or special fields.
func parseReleaseTitlePure(ctx context.Context, title string) (generated.ParseReleaseTitleResult, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return generated.ParseReleaseTitleResult{}, errors.New("release title is required")
	}
	tags := reltitletags.Extract(title)
	result := generated.ParseReleaseTitleResult{
		Facts: generated.TitleTagFacts{
			Schema: tags.Schema, Implementation: tags.Implementation, Tags: tags.Tags,
			ConfigDigest: stringPtr(tags.ConfigDigest), ValueTags: &tags.ValueTags, Diagnostic: stringPtr(tags.Diagnostic),
		},
		ParserVersion: tags.Implementation,
	}
	selection, err := parser.NewSelectionFromProfile(parser.ParserReltitle, nil, 0)
	if err != nil {
		return generated.ParseReleaseTitleResult{}, err
	}
	episode, err := parser.ParseEpisode(ctx, parser.DefaultRegistry(), selection.Parser, parser.EpisodeParseInput{Kind: parser.InputReleaseTitle, Text: title})
	if err != nil {
		return generated.ParseReleaseTitleResult{}, err
	}
	if episode.Episode != nil {
		number, err := strconv.ParseFloat(string(*episode.Episode), 32)
		if err != nil {
			return generated.ParseReleaseTitleResult{}, err
		}
		result.Episode = &generated.EpisodeParseResult{
			Matched: true, EpisodeNumber: float32(number), Evidence: stringPtr(episode.RawCapture),
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
