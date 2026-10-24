package parser

type namedParsePass struct {
	Name string
	Pass parsePass
}

// parsePasses run in this order after the pre-pass extractors in Parse. The order
// matters: later passes read what earlier ones left in the parseContext.
var parsePasses = []namedParsePass{
	{Name: "extract_pack", Pass: passExtractPack},
	{Name: "group_slot2_platform_infer", Pass: passGroupSlot2PlatformInfer},
	{Name: "group_subs_x2_override", Pass: passGroupSubsX2Override},
	{Name: "extract_tech_subs", Pass: passExtractTechSubs},
	{Name: "extract_tech_audio", Pass: passExtractTechAudio},
	{Name: "extract_bracket_title_aliases", Pass: passExtractBracketTitleAliases},
	{Name: "extract_tag_code_audio_lang", Pass: passExtractTagCodeAudioLang},
	{Name: "detect_season_sp_movie", Pass: passDetectSeasonSPMovie},
	{Name: "detect_episode_range_and_episode", Pass: passDetectEpisodeRangeAndEpisode},
}
