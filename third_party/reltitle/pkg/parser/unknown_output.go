package parser

import (
	"github.com/wangkayu/reltitle/pkg/model"
	"strings"
)

func isUnknownOutput(contentKind model.ContentKind, templateID string) bool {
	if contentKind.Canonical() == model.ContentKindUnknown {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(templateID), "TF_UNKNOWN_DEFAULT")
}

func normalizeContentKindByTemplateID(contentKind model.ContentKind, templateID string) model.ContentKind {
	tid := strings.TrimSpace(templateID)
	if tid == "" {
		return contentKind
	}
	cur := contentKind.Canonical()
	switch {
	case strings.HasPrefix(tid, "TF_BATCH_"):
		return model.ContentKindBatch
	case strings.HasPrefix(tid, "TF_MOVIE_"):
		if cur == "" || cur == model.ContentKindUnknown || cur == model.ContentKindTVEP {
			return model.ContentKindMovie
		}
		return contentKind
	case strings.HasPrefix(tid, "TF_SPECIAL_"), strings.HasPrefix(tid, "TF_SP_"):
		if cur == "" || cur == model.ContentKindUnknown {
			return model.ContentKindSpecial
		}
		return contentKind
	default:
		return contentKind
	}
}

func pruneUnknownCoreAttributes(attrs []model.Attribute) []model.Attribute {
	if len(attrs) == 0 {
		return attrs
	}
	out := make([]model.Attribute, 0, len(attrs))
	for _, a := range attrs {
		if !keepUnknownOutputAttribute(a) {
			continue
		}
		if strings.TrimSpace(a.Normalized) == "" {
			continue
		}
		out = append(out, a)
	}
	return dedupeAttributes(out)
}

func keepUnknownOutputAttribute(a model.Attribute) bool {
	switch a.Type {
	case model.AttrGroup, model.AttrTitle, model.AttrTitleAlias,
		model.AttrEdition, model.AttrReleaseStatus,
		model.AttrSourceType, model.AttrResolution, model.AttrVideoCodec,
		model.AttrAudioCodec, model.AttrBitDepth, model.AttrContainer,
		model.AttrPlatform, model.AttrPlatformRegion, model.AttrOriginRegion,
		model.AttrHDR, model.AttrCRC, model.AttrSubFlag, model.AttrSubLang,
		model.AttrSubFormat, model.AttrSubLangGroup, model.AttrAudioFlag,
		model.AttrAudioLang, model.AttrAudioChannels, model.AttrAudioObject:
		return true
	case model.AttrNote:
		return keepUnknownOutputNote(a.Normalized)
	default:
		return false
	}
}

func keepUnknownOutputNote(normalized string) bool {
	up := strings.ToUpper(strings.TrimSpace(normalized))
	switch up {
	case "RETAIL", "BOOTLEG", "USBD", "JPBD", "UHD", "BONUS_VIDEO", "RECRUITMENT":
		return true
	default:
		return false
	}
}
