package parser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Selection is the single automatic-pipeline parser choice. Offset remains
// outside the parser and is applied by the episode identity resolver.
type Selection struct {
	Parser            ParserSelection
	Descriptor        ParserDescriptor
	ConfigFingerprint string
	Offset            int
}

func NewSelection(kind ParserKind, config json.RawMessage, offset int) (Selection, error) {
	parserConfig := append(json.RawMessage(nil), config...)
	parserSelection := ParserSelection{ID: string(kind), Config: parserConfig}
	adapter, err := DefaultRegistry().ValidateSelection(parserSelection)
	if err != nil {
		return Selection{}, fmt.Errorf("validate parser selection: %w", err)
	}
	canonicalConfig, err := canonicalConfigJSON(parserConfig)
	if err != nil {
		return Selection{}, fmt.Errorf("canonicalize parser config: %w", err)
	}
	parserSelection.Config = canonicalConfig
	return Selection{
		Parser:            parserSelection,
		Descriptor:        adapter.Descriptor(),
		ConfigFingerprint: digestBytes(parserSelection.Config),
		Offset:            offset,
	}, nil
}

func canonicalConfigJSON(config json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(config)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing JSON value")
		}
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

// NilConfigFingerprint is the canonical config fingerprint of a selection
// whose profile carried no config: MarshalProfileConfig(nil) encodes the empty
// profile as NULL config, and NewSelection fingerprints the canonicalized
// (empty) config bytes. A frozen snapshot row with NULL parser_config is only
// trustworthy when its fingerprint equals this value; any other non-NULL
// fingerprint with NULL config contradicts the frozen evidence.
func NilConfigFingerprint() string { return digestBytes(nil) }

func (s Selection) Kind() ParserKind { return ParserKind(s.Parser.ID) }

// ProfileConfig is the parser-visible configuration derived from an instance's
// effective rules. It is the only shape the automatic pipeline passes to a
// parser adapter, so release-title and file-name inputs share one profile.
type ProfileConfig struct {
	EpisodePatterns []string `json:"episode_patterns"`
}

// NewSelectionFromProfile is the single automatic-pipeline selection builder.
// It combines the frozen instance parser-policy kind with the instance's
// effective episode patterns and validates the result against the authoritative
// recognition registry, so policy validation and runtime execution resolve the
// same adapter. The offset is NOT applied here: it is carried on the selection
// for the episode identity resolver to apply exactly once.
func NewSelectionFromProfile(kind ParserKind, episodePatterns []string, offset int) (Selection, error) {
	if kind == ParserReltitle {
		// reltitle takes no patterns: its behavior is the pinned lexicon, so a
		// template's patterns neither reach it nor perturb its config digest.
		episodePatterns = nil
	}
	config, err := MarshalProfileConfig(episodePatterns)
	if err != nil {
		return Selection{}, err
	}
	return NewSelection(kind, config, offset)
}

// MarshalProfileConfig encodes the effective episode patterns as the stable
// parser config. No patterns yields a nil config so the adapter uses its own
// documented defaults instead of an empty (and therefore never-matching) list.
func MarshalProfileConfig(episodePatterns []string) (json.RawMessage, error) {
	if len(episodePatterns) == 0 {
		return nil, nil
	}
	config, err := json.Marshal(ProfileConfig{EpisodePatterns: episodePatterns})
	if err != nil {
		return nil, fmt.Errorf("encode parser profile config: %w", err)
	}
	return config, nil
}
