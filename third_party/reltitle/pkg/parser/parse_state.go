package parser

import (
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

// parseState holds the values that Parser.Parse hands from one stage to the next.
//
// The first block is fixed once the pre-pass is done (rawTitle, tokens, ctx, ...);
// the rest is progressively filled in and refined by the stage methods in parser.go.
type parseState struct {
	originalRawTitle string
	rawTitle         string
	tokens           []model.Token
	ctx              *parseContext
	groupSpan        [2]int
	tailParenBlock   string
	version          string

	attrs   []model.Attribute
	group   string
	title   string
	aliases []string

	titleStart int
	titleEnd   int

	packKind        string
	season          string
	seasonIdx       int
	seasonRange     string
	spKind          string
	movieIndex      string
	movieIndexIdx   int
	movieIndexRange string
	hasMovieKeyword bool

	episode         string
	episodeIdx      int
	episodeList     string
	episodeRange    string
	episodeRangeIdx int
	epslot          *model.EPSlot

	hasTech bool
	hasYear bool

	movieRouteHardExcluded bool
	movieContentSignal     bool
	epRoute                string
	contentKind            model.ContentKind
	contentConf            float64
	templateID             string
	templateConf           float64
	routeID                string
}

// episodeAnchor is the right-most episode, episode-range or season token index (-1 if none).
func (st *parseState) episodeAnchor() int {
	anchor := st.episodeIdx
	if st.episodeRangeIdx > anchor {
		anchor = st.episodeRangeIdx
	}
	if st.seasonIdx > anchor {
		anchor = st.seasonIdx
	}
	return anchor
}

// clearEpisodeSignals drops the episode, episode list, episode range and the episode slot.
func (st *parseState) clearEpisodeSignals() {
	st.episode = ""
	st.episodeIdx = -1
	st.episodeList = ""
	st.episodeRange = ""
	st.episodeRangeIdx = -1
	st.epslot = nil
}

// reclassify recomputes the movie signals, the episode route and the content kind from the
// current state. Call it after anything that changes the episode, season or movie fields.
func (p *Parser) reclassify(st *parseState) {
	st.movieRouteHardExcluded = hasMovieRouteHardExclusion(st.rawTitle, st.attrs, st.episodeRange, st.season, st.seasonRange, st.packKind)
	st.movieContentSignal = hasMovieContentSignal(st.attrs, st.hasMovieKeyword, st.movieIndex, st.spKind)
	st.epRoute = computeEpRoute(st.tokens, st.episode, st.episodeList, st.episodeRange, st.season, st.seasonRange, st.spKind, st.movieIndex, st.hasMovieKeyword, st.movieRouteHardExcluded)
	st.contentKind, st.contentConf = inferContentKind(p.cfg, st.episode, st.episodeRange, st.season, st.seasonRange, st.packKind, st.spKind, st.movieIndex, st.movieIndexRange, st.hasTech, st.hasMovieKeyword, st.hasYear, st.movieRouteHardExcluded, st.movieContentSignal)
}

// normalizeMovieSignals reinterprets an episode number as a movie index when the title
// looks like a movie, and reclassifies if it did.
func (p *Parser) normalizeMovieSignals(st *parseState) {
	episode, movieIndex, episodeIdx, movieIndexIdx, changed := normalizeMovieEpisodeSignals(st.rawTitle, st.contentKind, st.episode, st.episodeRange, st.season, st.seasonRange, st.packKind, st.hasMovieKeyword, st.movieIndex, st.movieIndexRange, st.episodeIdx, st.movieIndexIdx)
	if !changed {
		return
	}
	st.episode, st.movieIndex, st.episodeIdx, st.movieIndexIdx = episode, movieIndex, episodeIdx, movieIndexIdx
	st.title = trimMovieDashEpisodeTailFromTitle(st.rawTitle, st.title, st.episode, st.movieIndex)
	st.epslot = nil
	p.reclassify(st)
}

// rematchTemplate matches the template again after a route override rewrote the episode
// fields, and keeps the result only when it is a real match.
func (p *Parser) rematchTemplate(st *parseState) {
	id, conf, _ := matchTemplate(p.cfg, st.epRoute, st.contentKind, st.group, st.episode, st.episodeRange, st.season, st.packKind, st.spKind, st.movieIndex, st.hasTech, st.hasMovieKeyword, st.attrs, st.tokens)
	if strings.TrimSpace(id) != "" && id != "TF_UNKNOWN_DEFAULT" {
		st.templateID = id
		st.templateConf = conf
	}
}

// detectTailGroupForAnchor returns the tail group of the tokens. When it sits at or before
// the episode/season anchor it is only accepted if another tail group follows the anchor.
func detectTailGroupForAnchor(tokens []model.Token, anchor int) (string, int) {
	tg, tgIdx := detectTailGroup(tokens)
	if tg != "" && anchor >= 0 && tgIdx >= 0 && tgIdx <= anchor {
		if tg2, tgIdx2 := detectTailGroupAfter(tokens, anchor); tg2 != "" {
			return tg2, tgIdx2
		}
		return "", -1
	}
	return tg, tgIdx
}

func newEPSlot(primary *model.EPEntity) *model.EPSlot {
	return &model.EPSlot{Primary: primary, Candidates: []model.EPEntity{*primary}}
}

// groupRouteNote is a note attribute produced while applying a group route override.
func groupRouteNote(text string) model.Attribute {
	return model.Attribute{Type: model.AttrNote, Normalized: text, TokenStart: -1, TokenEnd: -1, Confidence: 0.7, Source: model.AttrSourceGroupRouteOverride}
}
