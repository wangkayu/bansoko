package parser

import (
	"reflect"
	"testing"

	"github.com/wangkayu/reltitle/pkg/model"
	"github.com/wangkayu/reltitle/pkg/tokenize"
)

func TestRepeatedTitlePrefix(t *testing.T) {
	cases := []struct {
		slot, title string
		aliases     []string
		want        string
		wantAliases []string
	}{
		{"[Re：中文作品 第四季合集 / Re:English Title 4th - 01~11]", "中文作品", []string{"Re", "Re English Title 4th"}, "Re:中文作品", []string{"Re:English Title 4th"}},
		{"Same：中文作品 / same:English Title", "中文作品", []string{"Same", "English Title"}, "Same:中文作品", []string{"Same:English Title"}},
		{"Re：中文作品 / Other:English Title", "中文作品", []string{"Re"}, "中文作品", []string{"Re"}},
		{"Re：中文作品", "中文作品", []string{"Re"}, "中文作品", []string{"Re"}},
		{"中文作品 / Re:English Title", "中文作品", []string{"Re"}, "中文作品", []string{"Re"}},
		{"Re：中文作品 / Re:English Title", "另一作品", []string{"Re"}, "另一作品", []string{"Re"}},
		{"1080p：中文作品 / 1080p:English Title", "中文作品", []string{"1080p"}, "中文作品", []string{"1080p"}},
	}
	for _, tc := range cases {
		t.Run(tc.slot, func(t *testing.T) {
			tokens := []model.Token{}
			for _, text := range tokenize.Tokenize(tc.slot) {
				tokens = append(tokens, model.Token{Text: text})
			}
			title, aliases := restoreRepeatedTitlePrefix(tokens, 0, len(tokens), tc.title, tc.aliases)
			if title != tc.want || !reflect.DeepEqual(aliases, tc.wantAliases) {
				t.Fatalf("got %q %v want %q %v", title, aliases, tc.want, tc.wantAliases)
			}
		})
	}
}
