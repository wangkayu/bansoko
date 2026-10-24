package episodeidentity

import "testing"

func TestCanonicalEpisode(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		err  bool
	}{
		{"plain integer", "12", "12", false},
		{"leading zeroes", "0012", "12", false},
		{"decimal", "12.5", "12.5", false},
		{"decimal trailing zeroes", "12.500", "12.5", false},
		{"whole decimal", "12.0", "12", false},
		{"fraction only", "0.5", "0.5", false},
		{"zero", "0", "0", false},
		{"zero decimal", "0.00", "0", false},
		{"empty", "", "", true},
		{"negative", "-1", "", true},
		{"exponent", "1e2", "", true},
		{"non-numeric", "1a", "", true},
		{"space inside", "1 2", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalEpisode(tt.raw)
			if tt.err {
				if err == nil {
					t.Fatalf("CanonicalEpisode(%q) = %q, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CanonicalEpisode(%q) error: %v", tt.raw, err)
			}
			if string(got) != tt.want {
				t.Fatalf("CanonicalEpisode(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestEpisodeNumberAddOffset(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		offset int
		want   string
		err    bool
	}{
		{"zero offset", "12", 0, "12", false},
		{"positive offset", "12", 3, "15", false},
		{"negative offset", "12", -3, "9", false},
		{"fraction without offset is unchanged", "12.5", 0, "12.5", false},
		{"fraction takes no offset", "12.5", 2, "", true},
		{"negative result", "3", -5, "", true},
		{"bound overflow", "99999", 100000, "", true},
		{"offset bound", "1", 100001, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := CanonicalEpisode(tt.value)
			if err != nil {
				t.Fatalf("CanonicalEpisode(%q): %v", tt.value, err)
			}
			got, err := n.AddOffset(tt.offset)
			if tt.err {
				if err == nil {
					t.Fatalf("AddOffset(%s, %d) = %q, want error", tt.value, tt.offset, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("AddOffset(%s, %d) error: %v", tt.value, tt.offset, err)
			}
			if string(got) != tt.want {
				t.Fatalf("AddOffset(%s, %d) = %q, want %q", tt.value, tt.offset, got, tt.want)
			}
		})
	}
}
