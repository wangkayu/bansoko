// Structural types for the copy tables: every language's table must match the zh-CN key set,
//   with values widened to string.
//   Spelling: `export const enXxx = { ... } as const satisfies CopyShape<typeof zhXxx>;`
//   Missing and extra keys both fail at typecheck.

export type CopyShape<T> = {
  readonly [K in keyof T]: T[K] extends string ? string : CopyShape<T[K]>;
};
