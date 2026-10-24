// Package torrent contains the provider-independent torrent metadata parser.
// It is deliberately pure: HTTP fetching and provider facts stay in the
// acquisition application/adapters, while this package only turns bencode
// bytes into an immutable name/size/file tree snapshot.
package torrent

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"bansoko/internal/filekinds"
)

type File struct {
	Path      string
	Name      string
	SizeBytes int64
	MediaKind string
	Language  string
}

type Metadata struct {
	InfoHashV1     string
	InfoHashV2     string
	TorrentName    string
	TorrentVersion string
	TotalSize      int64
	Files          []File
}

type node struct {
	value any
	start int
	end   int
}

type decoder struct {
	data []byte
}

const maxNestingDepth = 128

// ImplementationVersion identifies this parser's output contract. It is
// recorded on every persisted torrent manifest projection so a stored file
// tree can always be attributed to the derivation that produced it, and a
// future parser change can be told apart from the current one.
const ImplementationVersion = "torrent-parser-v1"

// Parse decodes a v1 single/multi-file torrent and the common v2 file-tree
// metadata. The info hash is calculated from the exact bencoded info slice,
// as required by BitTorrent, rather than by re-encoding a Go map.
func Parse(data []byte) (Metadata, error) {
	if len(data) == 0 {
		return Metadata{}, errors.New("torrent payload is empty")
	}
	decoder := decoder{data: data}
	root, end, err := decoder.value(0, 0)
	if err != nil {
		return Metadata{}, fmt.Errorf("decode torrent bencode: %w", err)
	}
	if end != len(data) {
		return Metadata{}, errors.New("torrent payload has trailing bytes")
	}
	rootDict, ok := root.value.(map[string]node)
	if !ok {
		return Metadata{}, errors.New("torrent root must be a dictionary")
	}
	info, ok := rootDict["info"]
	if !ok {
		return Metadata{}, errors.New("torrent info dictionary is missing")
	}
	infoDict, ok := info.value.(map[string]node)
	if !ok {
		return Metadata{}, errors.New("torrent info must be a dictionary")
	}
	infoSlice := data[info.start:info.end]
	name := stringValue(infoDict, "name.utf-8", "name")
	if strings.TrimSpace(name) == "" {
		return Metadata{}, errors.New("torrent name is missing")
	}
	files, total, version, err := parseFiles(infoDict, name)
	if err != nil {
		return Metadata{}, err
	}
	if len(files) == 0 {
		return Metadata{}, errors.New("torrent contains no files")
	}
	result := Metadata{
		TorrentName:    name,
		TorrentVersion: version,
		TotalSize:      total,
		Files:          files,
	}
	if version == "v1" || version == "hybrid" {
		v1Hash := sha1.Sum(infoSlice)
		result.InfoHashV1 = hex.EncodeToString(v1Hash[:])
	}
	if version == "v2" || version == "hybrid" {
		v2Hash := sha256.Sum256(infoSlice)
		result.InfoHashV2 = hex.EncodeToString(v2Hash[:])
	}
	return result, nil
}

func (d decoder) value(offset, depth int) (node, int, error) {
	if depth > maxNestingDepth {
		return node{}, offset, errors.New("torrent bencode nesting exceeds limit")
	}
	if offset < 0 || offset >= len(d.data) {
		return node{}, offset, errors.New("unexpected end of torrent payload")
	}
	start := offset
	switch d.data[offset] {
	case 'i':
		end := bytesIndex(d.data, 'e', offset+1)
		if end < 0 {
			return node{}, offset, errors.New("unterminated torrent integer")
		}
		value, err := strconv.ParseInt(string(d.data[offset+1:end]), 10, 64)
		if err != nil {
			return node{}, offset, fmt.Errorf("invalid torrent integer: %w", err)
		}
		return node{value: value, start: start, end: end + 1}, end + 1, nil
	case 'l':
		items := make([]node, 0)
		cursor := offset + 1
		for cursor < len(d.data) && d.data[cursor] != 'e' {
			item, next, err := d.value(cursor, depth+1)
			if err != nil {
				return node{}, offset, err
			}
			items = append(items, item)
			cursor = next
		}
		if cursor >= len(d.data) {
			return node{}, offset, errors.New("unterminated torrent list")
		}
		return node{value: items, start: start, end: cursor + 1}, cursor + 1, nil
	case 'd':
		values := make(map[string]node)
		cursor := offset + 1
		for cursor < len(d.data) && d.data[cursor] != 'e' {
			key, next, err := d.value(cursor, depth+1)
			if err != nil {
				return node{}, offset, err
			}
			keyValue, ok := key.value.(string)
			if !ok {
				return node{}, offset, fmt.Errorf("torrent dictionary key is not a byte string at offset %d", cursor)
			}
			value, valueEnd, err := d.value(next, depth+1)
			if err != nil {
				return node{}, offset, err
			}
			values[keyValue] = value
			cursor = valueEnd
		}
		if cursor >= len(d.data) {
			return node{}, offset, fmt.Errorf("unterminated torrent dictionary at offset %d", offset)
		}
		return node{value: values, start: start, end: cursor + 1}, cursor + 1, nil
	default:
		if d.data[offset] < '0' || d.data[offset] > '9' {
			return node{}, offset, fmt.Errorf("invalid torrent token %q", d.data[offset])
		}
		colon := bytesIndex(d.data, ':', offset)
		if colon < 0 {
			return node{}, offset, errors.New("torrent byte string length is missing")
		}
		length, err := strconv.Atoi(string(d.data[offset:colon]))
		if err != nil || length < 0 {
			return node{}, offset, errors.New("invalid torrent byte string length")
		}
		end := colon + 1 + length
		if end > len(d.data) {
			return node{}, offset, errors.New("torrent byte string exceeds payload")
		}
		return node{value: string(d.data[colon+1 : end]), start: start, end: end}, end, nil
	}
}

func bytesIndex(data []byte, value byte, offset int) int {
	for index := offset; index < len(data); index++ {
		if data[index] == value {
			return index
		}
	}
	return -1
}

func parseFiles(info map[string]node, torrentName string) ([]File, int64, string, error) {
	if filesNode, ok := info["files"]; ok {
		items, ok := filesNode.value.([]node)
		if !ok {
			return nil, 0, "", errors.New("torrent files must be a list")
		}
		files := make([]File, 0, len(items))
		var total int64
		for _, item := range items {
			dict, ok := item.value.(map[string]node)
			if !ok {
				return nil, 0, "", errors.New("torrent file entry must be a dictionary")
			}
			length, ok := intValue(dict["length"])
			if !ok || length < 0 {
				return nil, 0, "", errors.New("torrent file length is invalid")
			}
			parts := stringList(dict, "path.utf-8", "path")
			relative, err := safeRelativePath(parts)
			if err != nil {
				return nil, 0, "", err
			}
			filePath := path.Join(torrentName, relative)
			files = append(files, newFile(filePath, length))
			total += length
		}
		version := "v1"
		if _, hasV2Tree := info["file tree"]; hasV2Tree {
			version = "hybrid"
		}
		return files, total, version, nil
	}
	if tree, ok := info["file tree"]; ok {
		files, total, err := parseV2FileTree(tree, torrentName, nil)
		if err != nil {
			return nil, 0, "", err
		}
		return files, total, "v2", nil
	}
	length, ok := intValue(info["length"])
	if !ok || length < 0 {
		return nil, 0, "", errors.New("torrent single-file length is invalid")
	}
	return []File{newFile(torrentName, length)}, length, "v1", nil
}

func parseV2FileTree(root node, torrentName string, prefix []string) ([]File, int64, error) {
	dict, ok := root.value.(map[string]node)
	if !ok {
		return nil, 0, errors.New("torrent v2 file tree node must be a dictionary")
	}
	keys := make([]string, 0, len(dict))
	for key := range dict {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	files := make([]File, 0)
	var total int64
	for _, key := range keys {
		child := dict[key]
		if key == "" {
			fileInfo, ok := child.value.(map[string]node)
			if !ok {
				return nil, 0, errors.New("torrent v2 file tree leaf must be a dictionary")
			}
			length, ok := intValue(fileInfo["length"])
			if !ok || length < 0 {
				return nil, 0, errors.New("torrent v2 file length is invalid")
			}
			relativeParts := append([]string(nil), prefix...)
			if len(relativeParts) == 0 {
				relativeParts = []string{torrentName}
			}
			relative, err := safeRelativePath(relativeParts)
			if err != nil {
				return nil, 0, err
			}
			files = append(files, newFile(relative, length))
			total += length
			continue
		}
		if key == "." || key == ".." || strings.ContainsAny(key, `/\\`) {
			return nil, 0, errors.New("torrent v2 file tree contains an unsafe component")
		}
		nextPrefix := append(append([]string(nil), prefix...), key)
		children, size, err := parseV2FileTree(child, torrentName, nextPrefix)
		if err != nil {
			return nil, 0, err
		}
		files = append(files, children...)
		total += size
	}
	return files, total, nil
}

func stringValue(dict map[string]node, keys ...string) string {
	for _, key := range keys {
		if value, ok := dict[key].value.(string); ok {
			return value
		}
	}
	return ""
}

func stringList(dict map[string]node, keys ...string) []string {
	for _, key := range keys {
		value, ok := dict[key]
		if !ok {
			continue
		}
		items, ok := value.value.([]node)
		if !ok {
			return nil
		}
		result := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.value.(string); ok {
				result = append(result, text)
			}
		}
		return result
	}
	return nil
}

func intValue(value node) (int64, bool) {
	if value.value == nil {
		return 0, false
	}
	result, ok := value.value.(int64)
	return result, ok
}

func safeRelativePath(parts []string) (string, error) {
	if len(parts) == 0 {
		return "", errors.New("torrent file path is empty")
	}
	cleanParts := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `/\\`) {
			return "", errors.New("torrent file path contains an unsafe component")
		}
		cleanParts = append(cleanParts, part)
	}
	return path.Join(cleanParts...), nil
}

func newFile(filePath string, size int64) File {
	name := path.Base(filePath)
	mediaKind := filekinds.ClassifyCurrent(name)
	language := ""
	if mediaKind == "subtitle" {
		base := strings.TrimSuffix(name, path.Ext(name))
		parts := strings.Split(base, ".")
		if len(parts) > 1 {
			language = strings.TrimSpace(parts[len(parts)-1])
		}
	}
	return File{Path: filePath, Name: name, SizeBytes: size, MediaKind: mediaKind, Language: language}
}
