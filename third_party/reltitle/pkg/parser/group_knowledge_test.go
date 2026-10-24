package parser

import (
	"github.com/wangkayu/reltitle/pkg/config"
	"testing"
)

func TestGroupKnowledge(t *testing.T) {
	// 创建测试配置
	cfg := &config.Config{
		GroupList: config.GroupListConfig{
			Groups: []config.GroupDef{
				{
					Name: "LoliHouse",
					Patterns: []string{
						"LoliHouse",
						"Lolihouse",
						"loli-house",
						"LOLIHOUSE",
					},
					Frequency:       "HIGH",
					ConfidenceBoost: 0.15,
				},
				{
					Name: "ANi",
					Patterns: []string{
						"ANi",
						"ani",
						"ANI",
					},
					Frequency:       "HIGH",
					ConfidenceBoost: 0.15,
				},
			},
		},
	}

	p := New(cfg)

	tests := []struct {
		name          string
		token         string
		shouldMatch   bool
		expectedGroup string
		expectedBoost float64
	}{
		{
			name:          "Exact match - LoliHouse",
			token:         "LoliHouse",
			shouldMatch:   true,
			expectedGroup: "LoliHouse",
			expectedBoost: 0.15,
		},
		{
			name:          "Case insensitive - lolihouse",
			token:         "lolihouse",
			shouldMatch:   true,
			expectedGroup: "LoliHouse",
			expectedBoost: 0.15,
		},
		{
			name:          "Alias match - Lolihouse",
			token:         "Lolihouse",
			shouldMatch:   true,
			expectedGroup: "LoliHouse",
			expectedBoost: 0.15,
		},
		{
			name:          "Hyphenated alias - loli-house",
			token:         "loli-house",
			shouldMatch:   true,
			expectedGroup: "LoliHouse",
			expectedBoost: 0.15,
		},
		{
			name:          "ANi exact",
			token:         "ANi",
			shouldMatch:   true,
			expectedGroup: "ANi",
			expectedBoost: 0.15,
		},
		{
			name:          "ANi lowercase",
			token:         "ani",
			shouldMatch:   true,
			expectedGroup: "ANi",
			expectedBoost: 0.15,
		},
		{
			name:        "Unknown group",
			token:       "UnknownGroup",
			shouldMatch: false,
		},
		{
			name:        "Empty token",
			token:       "",
			shouldMatch: false,
		},
		{
			name:        "Whitespace only",
			token:       "   ",
			shouldMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group, ok := p.isKnownGroup(tt.token)

			if ok != tt.shouldMatch {
				t.Errorf("isKnownGroup(%q) match = %v, want %v", tt.token, ok, tt.shouldMatch)
			}

			if tt.shouldMatch {
				if group == nil {
					t.Errorf("isKnownGroup(%q) returned nil group", tt.token)
					return
				}

				if group.Name != tt.expectedGroup {
					t.Errorf("isKnownGroup(%q) group name = %q, want %q", tt.token, group.Name, tt.expectedGroup)
				}

				if group.ConfidenceBoost != tt.expectedBoost {
					t.Errorf("isKnownGroup(%q) confidence boost = %f, want %f", tt.token, group.ConfidenceBoost, tt.expectedBoost)
				}
			}
		})
	}
}

func TestGroupKnowledgeIndexBuilding(t *testing.T) {
	cfg := &config.Config{
		GroupList: config.GroupListConfig{
			Groups: []config.GroupDef{
				{
					Name: "TestGroup",
					Patterns: []string{
						"TestGroup",
						"Test-Group",
						"TESTGROUP",
					},
					ConfidenceBoost: 0.10,
				},
			},
		},
	}

	p := New(cfg)

	// 验证索引构建正确
	expectedPatterns := []string{"testgroup", "test-group"}
	for _, pattern := range expectedPatterns {
		if _, ok := p.groupIndex[pattern]; !ok {
			t.Errorf("Expected pattern %q to be in groupIndex", pattern)
		}
	}

	// 验证所有 patterns 都指向同一个 GroupDef
	group1, _ := p.isKnownGroup("TestGroup")
	group2, _ := p.isKnownGroup("test-group")
	group3, _ := p.isKnownGroup("TESTGROUP")

	if group1.Name != "TestGroup" || group2.Name != "TestGroup" || group3.Name != "TestGroup" {
		t.Errorf("All patterns should point to the same group")
	}
}

func TestGroupKnowledgeEmptyConfig(t *testing.T) {
	// 测试空配置不会崩溃
	cfg := &config.Config{
		GroupList: config.GroupListConfig{
			Groups: nil,
		},
	}

	p := New(cfg)

	_, ok := p.isKnownGroup("AnyGroup")
	if ok {
		t.Errorf("Empty config should not match any group")
	}
}
