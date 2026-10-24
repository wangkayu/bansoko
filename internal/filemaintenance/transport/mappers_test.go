package transport

import (
	"testing"
	"time"

	filemaintenance "bansoko/internal/filemaintenance"
	"bansoko/internal/transport/httpapi/generated"
)

func TestPathDiffMappingPreservesOptionalExpectedPath(t *testing.T) {
	expected := "/library/season-1/episode-1.mkv"
	values := PathDiffViews([]filemaintenance.PathDiffView{
		{Differs: true, Disposition: "present", ExpectedPath: &expected, FilePath: "/downloads/episode-1.mkv"},
		{Differs: true, Disposition: "unresolved", FilePath: "/downloads/unknown.mkv"},
	})
	if len(values) != 2 || !values[0].Differs || values[0].ExpectedPath == nil || *values[0].ExpectedPath != expected {
		t.Fatalf("mapped expected path = %+v", values)
	}
	*values[0].ExpectedPath = "/mutated-response-path.mkv"
	if expected != "/library/season-1/episode-1.mkv" {
		t.Fatalf("application expected path was aliased: %q", expected)
	}
	if values[1].ExpectedPath != nil || values[1].Disposition != "unresolved" {
		t.Fatalf("mapped absent expected path = %+v", values[1])
	}
}

func TestFileEffectPolicyDefaultMapping(t *testing.T) {
	input := FileEffectPolicyDefaultInput(generated.FileEffectPolicyDefaultInput{
		ExpectedVersion:   7,
		UnclassifiedRoute: generated.UnclassifiedRoutePolicyValueRoute,
	})
	if input.ExpectedVersion != 7 || input.UnclassifiedRoute != filemaintenance.UnclassifiedRoutePolicyValueRoute {
		t.Fatalf("input=%+v", input)
	}
	view := FileEffectPolicyDefaultView(filemaintenance.FileEffectPolicyDefaultView{
		UnclassifiedRoute: filemaintenance.UnclassifiedRoutePolicyValueSkip,
		Version:           8,
	})
	if view.UnclassifiedRoute != generated.UnclassifiedRoutePolicyValueSkip || view.Version != 8 {
		t.Fatalf("view=%+v", view)
	}
}

func TestInstanceFileEffectPolicyMapping(t *testing.T) {
	input := InstanceFileEffectPolicyInput(generated.InstanceFileEffectPolicyInput{
		ExpectedVersion:   2,
		UnclassifiedRoute: generated.UnclassifiedRoutePolicyValueSkip,
	})
	if input.ExpectedVersion != 2 || input.UnclassifiedRoute != filemaintenance.UnclassifiedRoutePolicyValueSkip {
		t.Fatalf("input=%+v", input)
	}
	updatedAt := time.Unix(123, 0).UTC()
	view := InstanceFileEffectPolicyView(filemaintenance.InstanceFileEffectPolicyView{
		ManagedAnimeID:       "anime-1",
		SourceDefaultVersion: 4,
		UnclassifiedRoute:    filemaintenance.UnclassifiedRoutePolicyValueRoute,
		UpdatedAt:            updatedAt,
		Version:              5,
	})
	if view.ManagedAnimeId != generated.ID("anime-1") || view.SourceDefaultVersion != 4 || view.UnclassifiedRoute != generated.UnclassifiedRoutePolicyValueRoute || !view.UpdatedAt.Equal(updatedAt) || view.Version != 5 {
		t.Fatalf("view=%+v", view)
	}
}
