package httpapi

import (
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	animeworkspace "bansoko/internal/transport/animeworkspace"
	"bansoko/internal/transport/httpapi/generated"
)

func mapAnimeWorkspace(value readmodelapplication.AnimeWorkspace) (generated.AnimeWorkspace, error) {
	return animeworkspace.MapAnimeWorkspace(value)
}

func mapAnimeWorkspaceConfiguration(value readmodelapplication.AnimeWorkspaceConfiguration) (generated.AnimeWorkspaceConfiguration, error) {
	return animeworkspace.MapAnimeWorkspaceConfiguration(value)
}
