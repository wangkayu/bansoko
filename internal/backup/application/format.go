// Package application owns the database backup capability: a backup is an
// export the owner downloads and a restore is an upload of such a file. The
// app keeps no backup files of its own; temporary files live only for the
// duration of one export or restore. The backup never contains
// BANSOKO_SECRET_ENCRYPTION_KEY.
package application

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// File layout: the magic line, one JSON header line, then the pg_dump custom
// format archive (payload). The header carries what a restore checks before it
// touches anything: the exact schema the data belongs to and the payload's
// size and SHA-256.
const (
	Magic         = "BANSOKO-BACKUP/1\n"
	FormatVersion = 1
	AppName       = "bansoko"
	FileExtension = ".bansoko-backup"

	maxHeaderLineBytes = 4096
	fileTimeLayout     = "20060102T150405Z"
)

// Header is the backup file's metadata line.
type Header struct {
	App           string    `json:"app"`
	Format        int       `json:"format"`
	SchemaVersion string    `json:"schema_version"`
	CreatedAt     time.Time `json:"created_at"`
	PayloadBytes  int64     `json:"payload_bytes"`
	PayloadSHA256 string    `json:"payload_sha256"`
}

var (
	// schemaVersionPattern is a recorded migration file name such as
	// "038_drop_backup_runs.sql".
	schemaVersionPattern = regexp.MustCompile(`^([0-9]{3,6})_[a-z0-9_]{1,120}\.sql$`)
	sha256Pattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ErrInvalidFile reports a file that is not a backup of this app or whose
// header is malformed.
var ErrInvalidFile = errors.New("not a valid backup file")

// SchemaNumber returns the migration number of a recorded migration version.
func SchemaNumber(version string) (string, error) {
	match := schemaVersionPattern.FindStringSubmatch(version)
	if match == nil {
		return "", fmt.Errorf("migration version %q is not a migration file name", version)
	}
	return match[1], nil
}

// FileName is the download name of a backup created at createdAt for the
// given schema version: app name, UTC time and schema number.
func FileName(createdAt time.Time, schemaVersion string) string {
	number, err := SchemaNumber(schemaVersion)
	if err != nil {
		number = "unknown"
	}
	return fmt.Sprintf("%s-%s-schema%s%s", AppName, createdAt.UTC().Format(fileTimeLayout), number, FileExtension)
}

// Validate checks every header field.
func (h Header) Validate() error {
	switch {
	case h.App != AppName:
		return fmt.Errorf("%w: app is %q", ErrInvalidFile, h.App)
	case h.Format != FormatVersion:
		return fmt.Errorf("%w: unsupported format %d", ErrInvalidFile, h.Format)
	case !schemaVersionPattern.MatchString(h.SchemaVersion):
		return fmt.Errorf("%w: schema version is malformed", ErrInvalidFile)
	case h.CreatedAt.IsZero():
		return fmt.Errorf("%w: creation time is missing", ErrInvalidFile)
	case h.PayloadBytes <= 0:
		return fmt.Errorf("%w: payload size is not positive", ErrInvalidFile)
	case !sha256Pattern.MatchString(h.PayloadSHA256):
		return fmt.Errorf("%w: payload checksum is malformed", ErrInvalidFile)
	}
	return nil
}

// EncodeHeader returns the magic line and the header line.
func EncodeHeader(h Header) ([]byte, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	h.CreatedAt = h.CreatedAt.UTC()
	line, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	if len(line)+1 > maxHeaderLineBytes {
		return nil, errors.New("backup header is too long")
	}
	out := make([]byte, 0, len(Magic)+len(line)+1)
	out = append(out, Magic...)
	out = append(out, line...)
	out = append(out, '\n')
	return out, nil
}

// ReadHeader consumes the magic line and the header line from r, leaving r at
// the first payload byte. It reads at most a few kilobytes, so an arbitrary
// upload is rejected before any of it is stored.
func ReadHeader(r *bufio.Reader) (Header, error) {
	magic := make([]byte, len(Magic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != Magic {
		return Header{}, fmt.Errorf("%w: missing backup signature", ErrInvalidFile)
	}
	line := make([]byte, 0, 512)
	for {
		b, err := r.ReadByte()
		if err != nil {
			return Header{}, fmt.Errorf("%w: header line is incomplete", ErrInvalidFile)
		}
		if b == '\n' {
			break
		}
		if len(line) >= maxHeaderLineBytes {
			return Header{}, fmt.Errorf("%w: header line is too long", ErrInvalidFile)
		}
		line = append(line, b)
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var h Header
	if err := decoder.Decode(&h); err != nil {
		return Header{}, fmt.Errorf("%w: header is not valid JSON", ErrInvalidFile)
	}
	if decoder.More() || strings.TrimSpace(string(line)) != string(line) {
		return Header{}, fmt.Errorf("%w: header line has trailing content", ErrInvalidFile)
	}
	if err := h.Validate(); err != nil {
		return Header{}, err
	}
	return h, nil
}
