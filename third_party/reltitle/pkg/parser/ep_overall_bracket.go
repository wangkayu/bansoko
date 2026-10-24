package parser

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

// Only complete episode-slot brackets qualify, not occurrences inside titles
// or release notes. The optional left number must agree with the primary EP.
var overallEpisodeBracketPattern = regexp.MustCompile(`[\[【]\s*(?:(\d{1,4})\s*[-－]\s*)?(?:总第|總第)\s*(\d{1,4})(?:\s*[-~～－]\s*(\d{1,4}))?\s*(?:集|话|話)?\s*[\]】]`)

func detectOverallBracketAlternate(raw string, primary *model.EPReference) (model.EPReference, bool) {
	if primary == nil || primary.Axis.Canonical() != model.EPAxisSE {
		return model.EPReference{}, false
	}
	for _, m := range overallEpisodeBracketPattern.FindAllStringSubmatch(raw, -1) {
		left := strings.TrimLeft(m[1], "0")
		start, _ := strconv.Atoi(m[2])
		end, _ := strconv.Atoi(m[3])
		if start <= 0 {
			continue
		}
		ref := model.EPReference{Axis: model.EPAxisABS, Relation: "overall", Source: model.EPSourceCanonical, TokenStart: -1, TokenEnd: -1}
		if primary.Kind.Canonical() == model.EPKindEP && m[3] == "" {
			if left != "" && left != primary.Episode {
				continue
			}
			ref.Kind = model.EPKindEP
			ref.Episode = strconv.Itoa(start)
			return ref, true
		}
		if primary.Kind.Canonical() == model.EPKindRange && m[1] == "" && end >= start {
			blocks := bracketSegmentPattern.FindAllStringSubmatch(raw, -1)
			if len(blocks) < 3 || (strings.TrimSpace(blocks[0][1]) != "晚街与灯" && strings.TrimSpace(blocks[0][1]) != "晚街與燈") ||
				!strings.ContainsAny(blocks[1][1], "/／") ||
				(!strings.Contains(blocks[1][1], "合集") && !strings.Contains(blocks[1][1], "合輯")) ||
				blocks[2][0] != m[0] {
				continue
			}
			pStart, errStart := strconv.Atoi(primary.RangeStart)
			pEnd, errEnd := strconv.Atoi(primary.RangeEnd)
			if errStart != nil || errEnd != nil || end-start != pEnd-pStart {
				continue
			}
			ref.Kind = model.EPKindRange
			ref.RangeStart, ref.RangeEnd = strconv.Itoa(start), strconv.Itoa(end)
			return ref, true
		}
	}
	return model.EPReference{}, false
}
