import type { NamingRules } from "../../lib/api/client";

/** Whether two naming values' template bodies match (the legacy preset field is not compared).
 *
 *  A value IS the template body (`/media/${kind}`); the server adds the outer backticks,
 *  so they are not part of the value. */
export function sameNamingTemplates(a: NamingRules, b: NamingRules): boolean {
  return (
    a.language === b.language &&
    a.subtitle_suffix_template === b.subtitle_suffix_template &&
    (["tv", "movie", "unknown"] as const).every(
      (variant) =>
        a[variant].directory_template === b[variant].directory_template &&
        a[variant].video_filename_template === b[variant].video_filename_template,
    )
  );
}

/** Takes only the template body: selecting a template copies it whole, without a preset reference. */
export function namingTemplateBody(rules: NamingRules): NamingRules {
  return {
    language: rules.language,
    tv: { ...rules.tv },
    movie: { ...rules.movie },
    unknown: { ...rules.unknown },
    subtitle_suffix_template: rules.subtitle_suffix_template,
  };
}
