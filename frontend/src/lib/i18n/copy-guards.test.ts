import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";

// Static guards: the UI language can switch at runtime, so copy must be read at render/call time.
//   1. A module's top level never calls t()/tryT()/reason-code functions (otherwise the copy
//   freezes at the import-time language);
//   2. Source outside the copy tables contains no Chinese literals (comments excepted).

const SOURCE_ROOT = fileURLToPath(new URL("../..", import.meta.url));
const COPY_CALLS = new Set(["t", "tryT", "reasonLabel", "reasonView", "knownReasonLabel", "namingFailureDetail", "namingPendingLabel"]);
const CJK = /[\u3000-\u303f\u4e00-\u9fff\uff00-\uffef]/;

// The api/ layer's network fallback wording goes only into error "details"; ownerModel recognizes the server's fixed descriptions.
const CJK_ALLOWED = new Set(["lib/api/client.ts", "features/rss-list/ownerModel.tsx"]);

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const full = path.join(dir, name);
    if (statSync(full).isDirectory()) sourceFiles(full, out);
    else if (/\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) && !name.endsWith(".d.ts")) out.push(full);
  }
  return out;
}

const files = sourceFiles(SOURCE_ROOT)
  .map((file) => path.relative(SOURCE_ROOT, file).split(path.sep).join("/"))
  .filter((file) => !file.startsWith("lib/i18n/") && !file.startsWith("testing/") && file !== "lib/api/generated.ts");

function parse(file: string): ts.SourceFile {
  const full = path.join(SOURCE_ROOT, file);
  return ts.createSourceFile(full, readFileSync(full, "utf8"), ts.ScriptTarget.Latest, true, file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
}

function location(sf: ts.SourceFile, file: string, node: ts.Node): string {
  return `${file}:${sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1}`;
}

describe("copy guards", () => {
  it("does not read copy at module scope", () => {
    const found: string[] = [];
    for (const file of files) {
      const sf = parse(file);
      const visit = (node: ts.Node, inFunction: boolean) => {
        if (!inFunction && ts.isCallExpression(node) && ts.isIdentifier(node.expression) && COPY_CALLS.has(node.expression.text)) {
          found.push(location(sf, file, node));
        }
        const nested = inFunction || ts.isFunctionLike(node);
        ts.forEachChild(node, (child) => visit(child, nested));
      };
      visit(sf, false);
    }
    expect(found).toEqual([]);
  });

  it("keeps Chinese text in the copy catalog", () => {
    const found: string[] = [];
    for (const file of files) {
      if (CJK_ALLOWED.has(file)) continue;
      const sf = parse(file);
      const visit = (node: ts.Node) => {
        if ((ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node) || ts.isTemplateHead(node) || ts.isTemplateMiddle(node) || ts.isTemplateTail(node) || ts.isJsxText(node)) && CJK.test(node.text)) {
          found.push(location(sf, file, node));
        }
        ts.forEachChild(node, visit);
      };
      visit(sf);
    }
    expect(found).toEqual([]);
  });
});
