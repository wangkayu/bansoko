import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import postcss, { type Rule } from "postcss";
import { describe, expect, it } from "vitest";
import { buttonDisabled, buttonVariant, chipTone, statusBusyDot, statusDot, statusIcon, statusText, type StatePillTone } from "./tones";

// Token rules from design-language §2/§3 (UI review X2/X3/L1/L2), checked
// against the real theme values in index.css so they cannot drift back:
// - --overlay is a near-black backdrop in both themes, and the Dialog backdrop uses it;
// - StatePill dots/icons reach 3:1 non-text contrast and status/chip label ink 4.5:1 on the
//   grounds they sit on (attention tones over their own tint), in both themes,
//   and never use --accent;
// - --accent is never a button fill, and a disabled button is not opacity-only.

const read = (relative: string) => readFileSync(fileURLToPath(new URL(relative, import.meta.url)), "utf8");
const indexCSS = read("../../index.css");
const baseCSS = read("./base.css");

type Theme = "light" | "dark";
type HSLA = { h: number; s: number; l: number; a: number };

function themeTokens(): Record<Theme, Map<string, string>> {
  const root = postcss.parse(indexCSS);
  const tokens: Record<Theme, Map<string, string>> = { light: new Map(), dark: new Map() };
  root.walkRules((rule: Rule) => {
    const theme: Theme | undefined = rule.selector === ":root" ? "light" : rule.selector === ".dark" ? "dark" : undefined;
    if (!theme) return;
    rule.walkDecls((decl) => {
      if (decl.prop.startsWith("--")) tokens[theme].set(decl.prop.slice(2), decl.value.trim());
    });
  });
  return tokens;
}
const TOKENS = themeTokens();

function parseChannels(value: string): HSLA {
  const match = /^(-?[\d.]+)\s+([\d.]+)%\s+([\d.]+)%(?:\s*\/\s*([\d.]+))?$/.exec(value.replace(/\/\*.*?\*\//g, "").trim());
  if (!match) throw new Error(`not bare HSL channels: ${value}`);
  return { h: Number(match[1]), s: Number(match[2]), l: Number(match[3]), a: match[4] === undefined ? 1 : Number(match[4]) };
}

function token(theme: Theme, name: string): HSLA {
  const value = TOKENS[theme].get(name);
  if (value === undefined) throw new Error(`--${name} is not defined in the ${theme} theme`);
  return parseChannels(value);
}

function rgb({ h, s, l }: HSLA): [number, number, number] {
  const sat = s / 100;
  const light = l / 100;
  const k = (n: number) => (n + h / 30) % 12;
  const a = sat * Math.min(light, 1 - light);
  const f = (n: number) => light - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
  return [f(0), f(8), f(4)];
}

function luminance(color: [number, number, number]): number {
  const [r, g, b] = color.map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a: [number, number, number], b: [number, number, number]): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

function over(fg: [number, number, number], alpha: number, bg: [number, number, number]): [number, number, number] {
  return [0, 1, 2].map((i) => fg[i] * alpha + bg[i] * (1 - alpha)) as [number, number, number];
}

// Tailwind color name → theme token, from the @theme inline mapping in base.css.
const COLOR_TOKEN = new Map(
  [...baseCSS.matchAll(/--color-([\w-]+):\s*hsl\(var\(--([\w-]+)\)\)/g)].map((m) => [m[1], m[2]]),
);

// Grounds a tag or pill sits on: dialogs and cards, the page, muted rows.
const GROUNDS = ["card", "popover", "background", "muted"];
const MIN_NON_TEXT = 3;
const MIN_TEXT = 4.5;

function worstContrast(theme: Theme, tokenName: string, alpha: number): { ratio: number; ground: string } {
  const fg = rgb(token(theme, tokenName));
  let worst = { ratio: Infinity, ground: "" };
  for (const ground of GROUNDS) {
    const bg = rgb(token(theme, ground));
    const ratio = contrast(over(fg, alpha, bg), bg);
    if (ratio < worst.ratio) worst = { ratio, ground };
  }
  return worst;
}

/** `tw:bg-online` / `tw:bg-warning/12` / `tw:text-muted-foreground` → token + alpha (non-colour utilities such as `tw:text-xs` are skipped). */
function utilityColor(classes: string, utility: "bg" | "border" | "text"): { token: string; alpha: number } {
  for (const match of classes.matchAll(new RegExp(`(?:^|\\s)tw:${utility}-([a-z-]+?)(?:/(\\d+))?(?=\\s|$)`, "g"))) {
    const tokenName = COLOR_TOKEN.get(match[1]);
    if (tokenName) return { token: tokenName, alpha: match[2] === undefined ? 1 : Number(match[2]) / 100 };
  }
  throw new Error(`no token-mapped tw:${utility}-* color in "${classes}"`);
}

const THEMES: Theme[] = ["light", "dark"];
const TONES = Object.keys(statusDot) as StatePillTone[];

describe("theme tokens", () => {
  it.each(THEMES)("--overlay is a near-black backdrop at 55–60%% (%s)", (theme) => {
    const overlay = token(theme, "overlay");
    expect(overlay.l).toBeLessThanOrEqual(10);
    expect(overlay.a).toBeGreaterThanOrEqual(0.55);
    expect(overlay.a).toBeLessThanOrEqual(0.6);
  });

  it("the dialog backdrop uses --overlay, and nothing derives a ground from --foreground", () => {
    expect(COLOR_TOKEN.get("overlay")).toBe("overlay");
    expect(read("./Dialog.tsx")).toMatch(/BaseDialog\.Backdrop[\s\S]{0,200}tw:bg-overlay/);
    // Shadows may mix the ink; backgrounds and backdrops may not (dark ink is near-white).
    const groundsFromInk: string[] = [];
    postcss.parse(indexCSS).walkDecls(/^background(-color)?$/, (decl) => {
      if (/var\(--foreground\)/.test(decl.value)) groundsFromInk.push(`${(decl.parent as Rule).selector} { ${decl.toString()} }`);
    });
    expect(groundsFromInk).toEqual([]);
  });

  it("dark shadows are not mixed from the near-white ink", () => {
    for (const name of ["shadow-1", "shadow-2"]) {
      expect(TOKENS.dark.get(name), `--${name}`).toBeDefined();
      expect(TOKENS.dark.get(name)).not.toMatch(/var\(--foreground\)/);
    }
  });
});

describe("status and chip colours reach their contrast floors in both themes", () => {
  const cases = THEMES.flatMap((theme) => TONES.map((tone) => [theme, tone] as const));

  it.each(cases)("StatePill %s / %s: dot and icon reach 3:1, never --accent", (theme, tone) => {
    const dotClasses = statusDot[tone];
    const dot = tone === "muted" ? utilityColor(dotClasses, "border") : utilityColor(dotClasses, "bg");
    expect(dot.token, "--accent is never a status colour").not.toBe("accent");
    const worstDot = worstContrast(theme, dot.token, dot.alpha);
    expect(worstDot.ratio, `dot on ${worstDot.ground}`).toBeGreaterThanOrEqual(MIN_NON_TEXT);
    if (/(?:^|\s)tw:bg-/.test(statusText[tone])) {
      const tint = utilityColor(statusText[tone], "bg");
      for (const ground of GROUNDS) {
        const surface = over(rgb(token(theme, tint.token)), tint.alpha, rgb(token(theme, ground)));
        const ratio = contrast(over(rgb(token(theme, dot.token)), dot.alpha, surface), surface);
        expect(ratio, `dot on ${tone} tint over ${ground}`).toBeGreaterThanOrEqual(MIN_NON_TEXT);
      }
    }
    const icon = utilityColor(statusIcon[tone], "text");
    expect(worstContrast(theme, icon.token, icon.alpha).ratio).toBeGreaterThanOrEqual(MIN_NON_TEXT);
  });

  it.each(cases)("StatePill %s / %s: label ink reaches 4.5:1 on its own ground", (theme, tone) => {
    const ink = utilityColor(statusText[tone], "text");
    const tint = /(?:^|\s)tw:bg-/.test(statusText[tone]) ? utilityColor(statusText[tone], "bg") : undefined;
    for (const ground of GROUNDS) {
      const base = rgb(token(theme, ground));
      const surface = tint ? over(rgb(token(theme, tint.token)), tint.alpha, base) : base;
      const ratio = contrast(over(rgb(token(theme, ink.token)), ink.alpha, surface), surface);
      expect(ratio, `${tone} label on ${ground}`).toBeGreaterThanOrEqual(MIN_TEXT);
    }
  });

  it("the busy dot is neutral and only animates when motion is allowed", () => {
    expect(statusBusyDot).toContain("tw:motion-safe:animate-pulse");
    expect(statusBusyDot).not.toMatch(/(?:^|\s)tw:animate-/);
    expect(utilityColor(statusBusyDot, "bg").token).toBe("muted-foreground");
  });

  it.each(THEMES.flatMap((theme) => (Object.keys(chipTone) as (keyof typeof chipTone)[]).map((tone) => [theme, tone] as const)))(
    "Chip %s / %s: filled, no outline, ink reaches 4.5:1 on its fill",
    (theme, tone) => {
      const classes = chipTone[tone];
      expect(classes).not.toMatch(/(?:^|\s)tw:border(?:-|\s|$)/);
      const fill = utilityColor(classes, "bg");
      const ink = utilityColor(classes, "text");
      const surface = rgb(token(theme, fill.token));
      expect(contrast(rgb(token(theme, ink.token)), surface)).toBeGreaterThanOrEqual(MIN_TEXT);
    },
  );
});

describe("button hierarchy", () => {
  it("--accent is never a button fill", () => {
    for (const classes of [...Object.values(buttonVariant), ...Object.values(buttonDisabled)]) {
      expect(classes).not.toMatch(/(?:^|\s)tw:(?:[\w-]+:)*bg-accent(?:\/|\s|$)/);
    }
  });

  it("a disabled button has its own look, not opacity only", () => {
    for (const classes of Object.values(buttonDisabled)) {
      expect(classes).not.toMatch(/opacity/);
      expect(classes).toContain("tw:text-muted-foreground");
      expect(classes).not.toMatch(/hover:/);
    }
    expect(buttonDisabled.primary).toContain("tw:bg-muted");
    expect(buttonDisabled.danger).toContain("tw:bg-muted");
  });

  it("no stylesheet dims a disabled control with opacity", () => {
    const dimmed: string[] = [];
    postcss.parse(indexCSS).walkRules((rule) => {
      if (!/:disabled|\[disabled\]|\[aria-disabled/.test(rule.selector)) return;
      rule.walkDecls("opacity", (decl) => {
        dimmed.push(`${rule.selector} { ${decl.toString()} }`);
      });
    });
    expect(dimmed).toEqual([]);
  });
});
