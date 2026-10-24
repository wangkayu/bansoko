import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import postcss, { type AtRule, type ChildNode } from "postcss";
import { describe, expect, it } from "vitest";

// Cascade-layer contract: unlayered rules beat every @layer rule, so an
// unlayered element/universal selector in index.css would silently override tw:
// utilities. Resets live in `@layer base`. What may stay unlayered is a frozen,
// shrink-only list of selector families.

const read = (relative: string) => readFileSync(fileURLToPath(new URL(relative, import.meta.url)), "utf8");
const LAYER_ORDER = "properties, theme, base, utilities";

function unlayeredSelectors(nodes: ChildNode[]): string[] {
  const selectors: string[] = [];
  for (const node of nodes) {
    if (node.type === "rule") selectors.push(...node.selectors);
    if (node.type === "atrule" && node.name !== "layer" && node.nodes) selectors.push(...unlayeredSelectors(node.nodes));
  }
  return selectors;
}

// Shrink-only allowlist of unlayered selector families in index.css. A family
// is the token blocks (`:root`, `.dark`, matched as whole selectors), a
// type-scale class (`.text-title`), or a semantic class family by its first
// name segment (`.settings-*` → `.settings`, `.catalog-fact-*` → `.catalog`). Adding a family fails; so does keeping an entry whose
// family has no rules left — delete it in the same change that removes the
// last rule. New styling uses `tw:` utilities, not new index.css families.
const UNLAYERED_FAMILIES = [
  ":root",
  ".dark",
  ".text-badge",
  ".text-body",
  ".text-caption",
  ".text-title",
  ".account",
  ".anime",
  ".app",
  ".auth",
  ".break",
  ".browse",
  ".business",
  ".candidate",
  ".card",
  ".catalog",
  ".chain",
  ".description",
  ".draft",
  ".ellipsis",
  ".file",
  ".global",
  ".icon",
  ".instance",
  ".log",
  ".metadata",
  // The phone "More" sheet rows.
  ".more",
  ".naming",
  ".offset",
  ".policy",
  ".poster",
  ".preview",
  ".row",
  ".rss",
  ".rules",
  ".save",
  ".season",
  ".secret",
  ".seed",
  ".settings",
  ".sources",
  ".sr",
  ".stage",
  ".subgroups",
  ".system",
  // The phone floating tab bar.
  ".tabbar",
  ".text",
  ".title",
  ".top",
];

const TYPE_SCALE = new Set([".text-badge", ".text-body", ".text-caption", ".text-title"]);

function unlayeredFamily(selector: string): string {
  const leading = selector.match(/^(:root\b|\.[A-Za-z0-9_-]+)/)?.[1];
  if (!leading) return selector;
  if (leading === ":root" || leading === ".dark") return selector;
  if (TYPE_SCALE.has(leading)) return leading;
  return `.${leading.slice(1).split("-")[0]}`;
}

describe("index.css cascade layers", () => {
  const root = postcss.parse(read("../../index.css"));

  it("先声明层顺序 base → utilities", () => {
    const first = root.nodes.find((node) => node.type !== "comment") as AtRule;
    expect(first.type).toBe("atrule");
    expect(first.name).toBe("layer");
    expect(first.params).toBe(LAYER_ORDER);
  });

  it("重置与元素默认样式都在 @layer base 内", () => {
    const baseSelectors = root.nodes
      .filter((node): node is AtRule => node.type === "atrule" && node.name === "layer" && node.params === "base")
      .flatMap((layer) => layer.nodes?.flatMap((node) => (node.type === "rule" ? node.selectors : [])) ?? []);
    expect(baseSelectors).toEqual(expect.arrayContaining(["button", "*", "body", "h1"]));
  });

  it("不在 layer 中的规则只有类选择器和 :root 令牌", () => {
    const offenders = unlayeredSelectors(root.nodes).filter((selector) => !/^(\.|:root\b)/.test(selector));
    expect(offenders).toEqual([]);
  });

  it("未分层的选择器家族只减不增：不出现新家族", () => {
    const allowed = new Set(UNLAYERED_FAMILIES);
    const added = [...new Set(unlayeredSelectors(root.nodes).map(unlayeredFamily))].filter((family) => !allowed.has(family));
    expect(added, "new unlayered selector families in index.css — use tw: utilities instead").toEqual([]);
  });

  it("未分层的选择器家族只减不增：已无规则的家族必须从清单删除", () => {
    const present = new Set(unlayeredSelectors(root.nodes).map(unlayeredFamily));
    const stale = UNLAYERED_FAMILIES.filter((family) => !present.has(family));
    expect(stale, "allowlist entries with no rules left — delete them from UNLAYERED_FAMILIES").toEqual([]);
  });

  it("家族归类：令牌块按整条选择器，语义类按首段", () => {
    expect(unlayeredFamily(":root")).toBe(":root");
    expect(unlayeredFamily(".dark")).toBe(".dark");
    expect(unlayeredFamily(".dark .settings-nav")).toBe(".dark .settings-nav");
    expect(unlayeredFamily(".settings-nav-item:hover")).toBe(".settings");
    expect(unlayeredFamily(".catalog-fact-row > span")).toBe(".catalog");
    expect(unlayeredFamily(".text-caption")).toBe(".text-caption");
    expect(unlayeredFamily(".brand-new-family")).toBe(".brand");
  });
});

describe("base.css", () => {
  const source = read("./base.css");

  it("Tailwind 工具类进入 @layer utilities，层顺序与 index.css 一致", () => {
    expect(source).toContain(`@layer ${LAYER_ORDER};`);
    expect(source).toMatch(/@import "tailwindcss\/utilities\.css" layer\(utilities\) prefix\(tw\);/);
    expect(source).toMatch(/@import "tailwindcss\/theme\.css" layer\(theme\) prefix\(tw\);/);
  });
});
