import type { BusinessPresetView } from "../lib/api/client";

/** A complete, valid Global business preset for component tests. */
export const preset: BusinessPresetView = { digest: `sha256:${"a".repeat(64)}`, tag_filters: { anibt: { require: [], exclude: [] }, title: { require: [], exclude: [] } }, snapshot: {
 schema_version: "business-snapshot-v1",
 sections: { subgroups:{max_rss:7,entries:[]},parser:{episode_patterns:[]},release_rules:{must_contain:"",not_in:""},file_rules:{must_contain:"",not_in:""},naming_rules:{language:"js-naming-1",unknown:{directory_template:"",video_filename_template:""},tv:{directory_template:"tv",video_filename_template:"video"},movie:{directory_template:"movie",video_filename_template:"movie"},subtitle_suffix_template:"sub"},file_effect:{operation:"move",collision_policy:"fail"} },
 section_versions:{subgroups:1,parser:2,release_rules:3,file_rules:4,naming_rules:5,file_effect:6},section_schemas:{subgroups:"subgroups-v1",parser:"parser-section-v1",release_rules:"release-rules-v1",file_rules:"file-rules-v1",naming_rules:"naming-v1",file_effect:"file-effect-v1"},
 parser:{schema_version:"parser-policy-envelope-v1",kind:"regex",policy_version:8,provenance:"global",profile_version:2,rules_owner_version:2,source_owner:"global"},
 replacement:{admission_mode:"single_copy",quality_profile:{version:"profile-v1",items:[{key:"subgroup.score",ranks:[]},{key:"release.resolution",ranks:[["1080p"],["720p"]]}]}},replacement_version:9,unclassified_route:"route",file_effect_version:10,
 programs:{vars:{digest:`sha256:${"b".repeat(64)}`,version:`naming-vars-v1@sha256:${"b".repeat(64)}`},attrrules:{digest:`sha256:${"c".repeat(64)}`,version:`release-facts-v1@sha256:${"c".repeat(64)}`}}
} };
