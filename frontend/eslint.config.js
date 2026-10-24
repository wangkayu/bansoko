import js from '@eslint/js';
import tseslint from 'typescript-eslint';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';

const antdImports = {
  group: ['antd', 'antd/*', '@ant-design/*'],
  message: 'antd has been removed: use ui/base (or the ui wrappers) instead.',
};
// Icons are lucide-react only (styling contract S5).
const otherIconImports = {
  group: [
    'react-icons',
    'react-icons/*',
    '@heroicons/*',
    '@tabler/icons',
    '@tabler/icons-react',
    '@phosphor-icons/*',
    '@radix-ui/react-icons',
    '@mui/icons-material',
    '@mui/icons-material/*',
    '@iconify/*',
    'react-feather',
  ],
  message: 'Icons come from lucide-react only.',
};
const baseUIImports = {
  group: ['@base-ui/react', '@base-ui/react/*'],
  message: 'Import Base UI only inside src/ui/base; business code uses the ui/base wrappers.',
};

// Colors come only from the token-mapped utilities (styling contract S2):
// no `dark:` variant (dark mode is the .dark token values), no Tailwind
// palette classes, no arbitrary color values. Matched in string literals and
// template chunks, where class strings live.
const COLOR_UTILITY =
  '(?:bg|text|border(?:-[xytrblse])?|ring(?:-offset)?|outline|fill|stroke|from|via|to|decoration|divide|placeholder|caret|accent|shadow|inset-shadow)';
const PALETTE =
  '(?:(?:red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|slate|gray|zinc|neutral|stone)-\\d{2,3}|black|white)';
const colorSyntaxBans = [
  {
    pattern: 'tw:(?:[\\w-]+:)*dark:',
    message: 'The dark: variant is forbidden: dark mode is driven by the .dark token values.',
  },
  {
    pattern: `tw:(?:[\\w-]+:)*${COLOR_UTILITY}-${PALETTE}(?![\\w-])`,
    message: 'Tailwind palette colors are forbidden: use the token-mapped utilities (tw:bg-muted, tw:text-destructive, ...).',
  },
  {
    pattern: `tw:(?:[\\w-]+:)*${COLOR_UTILITY}-(?:\\[(?:#|rgba?\\(|hsla?\\(|hwb\\(|oklch\\(|oklab\\(|lab\\(|lch\\(|color|var\\()|\\(--)`,
    message: 'Arbitrary color values are forbidden: add a token in index.css and map it in base.css @theme inline.',
  },
  {
    // Backdrops and grounds never derive from --foreground: it is near-white in
    // dark, so a foreground-tinted layer lifts the page instead of dimming it
    // (UI review L1). Dialog backdrops use tw:bg-overlay.
    pattern: 'tw:(?:[\\w-]+:)*bg-foreground(?![\\w-])',
    message: 'bg-foreground is forbidden: backdrops use tw:bg-overlay, hover tints use tw:bg-muted or tw:bg-accent.',
  },
].flatMap(({ pattern, message }) => [
  { selector: `Literal[value=/${pattern}/]`, message },
  { selector: `TemplateElement[value.raw=/${pattern}/]`, message },
]);

// A disabled control has its own look (ui/base Button's buttonDisabled):
// business code never dims a control with opacity alone (UI review X2/C3).
const disabledOpacityPattern = 'tw:(?:[\\w-]+:)*(?:disabled|data-disabled|aria-disabled|has-data-disabled):(?:[\\w-]+:)*opacity-';
const disabledOpacityBans = [
  { selector: `Literal[value=/${disabledOpacityPattern}/]`, message: 'Opacity-only disabled styles are forbidden in business code: use the ui/base control\'s disabled state (muted ground and ink).' },
  { selector: `TemplateElement[value.raw=/${disabledOpacityPattern}/]`, message: 'Opacity-only disabled styles are forbidden in business code: use the ui/base control\'s disabled state (muted ground and ink).' },
];

const baseSyntaxBans = [
  {
    selector: "JSXOpeningElement[name.name='style']",
    message:
      'In-component <style> blocks are forbidden (token escape). Use ui components and index.css tokens.',
  },
  {
    selector: 'Literal[value=/text-\\[\\d+px\\]/]',
    message:
      'Arbitrary px font sizes are forbidden. Use the type scale: text-display/title/body/caption/badge.',
  },
  ...colorSyntaxBans,
];

// Business code uses the ui/base controls, never native form elements
// (styling contract S5). ui/base itself is exempt.
const nativeControlBan = {
  selector: "JSXOpeningElement[name.name=/^(input|select|textarea)$/]",
  message: 'Native <input>/<select>/<textarea> are forbidden in business code: use Input/Select/Textarea/Checkbox from ui/base.',
};

// Tests and e2e locate UI by role/label, never by component-library classes.
const antSelectorBans = [
  { selector: 'Literal[value=/\\.ant-[a-z]/]', message: 'antd class selectors (.ant-*) are forbidden in tests: query by role, name or label.' },
  { selector: 'TemplateElement[value.raw=/\\.ant-[a-z]/]', message: 'antd class selectors (.ant-*) are forbidden in tests: query by role, name or label.' },
];

export default tseslint.config(
  // Generated PWA/service-worker output and Vite's transient config module
  // are build artifacts, not source owned by the frontend lint gate.
  { ignores: ['dist', 'dev-dist', 'node_modules', '**/*.timestamp-*.mjs'] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2020,
      globals: {
        browser: true,
        es2020: true,
      },
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'react-refresh/only-export-components': [
        'warn',
        { allowConstantExport: true },
      ],
      '@typescript-eslint/no-explicit-any': 'error',
      '@typescript-eslint/no-unused-vars': 'off',
      'no-useless-escape': 'off',
      'no-case-declarations': 'off',
      'no-empty': 'off',
      '@typescript-eslint/no-require-imports': 'off',
      'prefer-const': 'off',
      '@typescript-eslint/no-empty-object-type': 'off',
      // Design-system bans (implementation plan S1 / T1a.5):
      'no-alert': 'error',
      'no-restricted-imports': ['error', { patterns: [antdImports, otherIconImports, baseUIImports] }],
      'no-restricted-syntax': ['error', ...baseSyntaxBans, nativeControlBan, ...disabledOpacityBans],
    },
  },
  {
    // The wrapper layer is the only place that touches Base UI primitives and
    // the native form elements behind the controls.
    files: ['src/ui/base/**/*.{ts,tsx}'],
    rules: {
      'no-restricted-imports': ['error', { patterns: [antdImports, otherIconImports] }],
      'no-restricted-syntax': ['error', ...baseSyntaxBans],
    },
  },
  {
    // Test files and test-only helpers are never in the dev server's module
    // graph, so the Fast Refresh export constraint does not apply to them.
    files: ['**/*.test.{ts,tsx}', 'src/testing/**/*.{ts,tsx}'],
    rules: {
      'react-refresh/only-export-components': 'off',
    },
  },
  {
    // Test harnesses may render native elements; they still may not select by antd classes.
    files: ['**/*.test.{ts,tsx}', 'e2e/**/*.ts'],
    rules: {
      'no-restricted-syntax': ['error', ...baseSyntaxBans, ...antSelectorBans],
    },
  },
);
