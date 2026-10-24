package naming

import (
	"fmt"
	"sort"
)

// Guard only complete interpolation expressions, after analysis of the user's
// source. Null must be handled by the user's expression before JavaScript's
// template-literal string coercion. Nested literal insertions are applied from
// right to left so source offsets stay valid. The internal helper is not part
// of the public catalog or the source identifier allowlist.
func guardedInterpolations(source string, ranges [][2]int) (string, error) {
	type insertion struct {
		offset int
		text   string
	}
	edits := make([]insertion, 0, len(ranges)*2)
	seen := map[[2]int]bool{}
	for _, span := range ranges {
		if seen[span] {
			continue
		}
		seen[span] = true
		if span[0] < 0 || span[1] > len(source) || span[0] >= span[1] {
			return "", fmt.Errorf("invalid interpolation source span")
		}
		edits = append(edits, insertion{span[0], "__namingValue(("}, insertion{span[1], "))"})
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].offset > edits[j].offset })
	for _, edit := range edits {
		source = source[:edit.offset] + edit.text + source[edit.offset:]
	}
	if len(source) > MaxTemplateBytes*4 {
		return "", fmt.Errorf("guarded naming expression exceeds byte limit")
	}
	return source, nil
}
