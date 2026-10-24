package parser

import "github.com/wangkayu/reltitle/pkg/model"

type parseContext struct {
	RawTitle        string
	Group           string
	AliasValidFunc  func(string) bool
	GroupSlot2Rules []groupSlot2PlatformRule

	PackKind       string
	TailParenBlock string

	AudioSlot *model.AudioSlot

	// Canonical maps for audio/video codec normalization (from config)
	AudioCodecCanonical map[string]string

	Season      string
	SeasonIdx   int
	SeasonRange string

	EpisodeFromSxxExx string
	SxxExxIdx         int

	SPKind             string
	MovieIndex         string
	MovieIndexIdx      int
	MovieIndexRange    string
	MovieIndexRangeIdx int
	HasMovieKeyword    bool

	EpisodeRange    string
	EpisodeRangeIdx int
	EpisodeList     string
	EpisodeListIdx  int

	Episode           string
	EpisodeIdx        int
	EpisodeVersion    string
	EpisodeVersionIdx int

	EPSlot *model.EPSlot

	EPDecision *epDecision

	ForcedTitle      string
	ForcedAliases    []string
	ForcedTitleStart int
	ForcedTitleEnd   int
}

type parsePass func(tokens []model.Token, attrs *[]model.Attribute, ctx *parseContext)

func newParseContext(rawTitle string, packKind string, tailParenBlock string) *parseContext {
	return &parseContext{
		RawTitle:           rawTitle,
		PackKind:           packKind,
		TailParenBlock:     tailParenBlock,
		ForcedTitleStart:   -1,
		ForcedTitleEnd:     -1,
		SeasonIdx:          -1,
		SxxExxIdx:          -1,
		MovieIndexIdx:      -1,
		MovieIndexRangeIdx: -1,
		EpisodeIdx:         -1,
		EpisodeRangeIdx:    -1,
		EpisodeListIdx:     -1,
		EpisodeVersionIdx:  -1,
		EPDecision:         newEPDecision(),
	}
}
