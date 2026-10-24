package filesystem

import (
	"bansoko/internal/fileidentity"
	"bansoko/internal/library/domain"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
)

func PublicationMarkerPath(target, token string) (string, error) {
	if token == "" || strings.ContainsAny(token, "/\\\x00") || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return "", errors.New("invalid publication marker identity")
	}
	return filepath.Join(filepath.Dir(target), ".bansoko-publication-"+token+".json"), nil
}

// WritePublicationMarker creates an immutable, descriptor-relative O_EXCL
// entry in a no-follow directory chain and fsyncs both file and parent.
func WritePublicationMarker(target, token string, raw []byte) (domain.FileIdentity, error) {
	p, err := PublicationMarkerPath(target, token)
	if err != nil {
		return domain.FileIdentity{}, err
	}
	if !json.Valid(raw) || len(raw) > 1<<20 {
		return domain.FileIdentity{}, errors.New("invalid marker envelope")
	}
	parent, err := openVerifiedDirectory("", filepath.Dir(p), false)
	if err != nil {
		return domain.FileIdentity{}, err
	}
	defer parent.Close()
	entry := verifiedEntry{parent: parent, name: filepath.Base(p), canonical: p}
	f, err := atomicCreateNoReplaceVerified(entry, entry.name)
	if err != nil {
		return domain.FileIdentity{}, err
	}
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return domain.FileIdentity{}, err
	}
	if err = f.Sync(); err != nil {
		return domain.FileIdentity{}, err
	}
	identity, err := fileidentity.FromFile(f)
	if err != nil {
		return identity, err
	}
	return identity, parent.Sync()
}

// ReadPublicationMarker returns evidence only, not quiescence. Refuses symlinks,
// nonregular entries, oversized data and replacement during the read.
func ReadPublicationMarker(target, token string) ([]byte, domain.FileIdentity, error) {
	p, err := PublicationMarkerPath(target, token)
	if err != nil {
		return nil, domain.FileIdentity{}, err
	}
	parent, err := openVerifiedDirectory("", filepath.Dir(p), false)
	if err != nil {
		return nil, domain.FileIdentity{}, err
	}
	defer parent.Close()
	entry := verifiedEntry{parent: parent, name: filepath.Base(p), canonical: p}
	f, err := openEntryNoFollow(entry)
	if err != nil {
		return nil, domain.FileIdentity{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return nil, domain.FileIdentity{}, errors.New("invalid publication marker file")
	}
	before, err := fileidentity.FromFile(f)
	if err != nil {
		return nil, before, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, before, err
	}
	current, err := openEntryNoFollow(entry)
	if err != nil {
		return nil, before, err
	}
	defer current.Close()
	after, err := fileidentity.FromFile(current)
	if err != nil || !fileidentity.Matches(before, after) {
		return nil, before, errors.New("publication marker replaced")
	}
	if !json.Valid(raw) {
		return nil, before, errors.New("invalid publication marker JSON")
	}
	return raw, before, nil
}
