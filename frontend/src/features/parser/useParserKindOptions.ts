import { useQuery } from "@tanstack/react-query";
import { listParserCatalog } from "../../lib/api/client";
import { configKeys } from "../../lib/queryKeys";
import { parserKindOptions } from "../../lib/parserKind";

/**
 * The parsers offering one capability from the catalog, in catalog order:
 * release_title for a one-shot download's RSS title, file_name for the file
 * manager's recognition of a file's own name. A single option is no choice at
 * all.
 */
export function useParserKindOptions(enabled: boolean, current: string | undefined, capability = "release_title") {
  const catalog = useQuery({ queryKey: configKeys.parserCatalog, queryFn: listParserCatalog, enabled, staleTime: Infinity });
  return parserKindOptions(catalog.data, current, capability);
}
