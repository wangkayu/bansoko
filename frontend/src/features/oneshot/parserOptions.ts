import { useQuery } from "@tanstack/react-query";
import { listParserCatalog } from "../../lib/api/client";
import { configKeys } from "../../lib/queryKeys";
import { parserKindOptions } from "../../lib/parserKind";

/** The parsers a one-shot download offers (those with release_title capability, in catalog order). A single option is no choice at all. */
export function useOneShotParserOptions(enabled: boolean, current: string | undefined) {
  const catalog = useQuery({ queryKey: configKeys.parserCatalog, queryFn: listParserCatalog, enabled, staleTime: Infinity });
  return parserKindOptions(catalog.data, current);
}

