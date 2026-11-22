// The tw: class strings of the subscribed wall's one-row-per-day layout (the design language's
//   Subscribed wall). The row label pins at the line start with the cards in a horizontal scrolling
//   track by default, and long rows can be expanded in place into a multi-column grid (2 columns on
//   phones, auto-fill poster columns on wider screens). The strings stay literal so the Tailwind
//   scanner can see them.

/** The outer container of every day row. */
export const WALL_ROWS_CLASS = "tw:grid tw:gap-2 tw:sm:gap-3";

/** A row: a single phone column (header above); at >=640px the header takes a 4.5rem column. Card width passes to the card item via --wall-card-w. */
export const WALL_ROW_CLASS =
  "tw:group/wall-row tw:grid tw:min-w-0 tw:grid-cols-[minmax(0,1fr)] tw:gap-1 tw:[--wall-card-w:10rem] tw:[--wall-gap:0.75rem] tw:sm:grid-cols-[4.5rem_minmax(0,1fr)] tw:sm:items-start tw:sm:gap-x-2 tw:sm:[--wall-card-w:11.5rem] tw:lg:[--wall-card-w:12.5rem]";

export const WALL_ROW_HEAD_CLASS =
  "tw:flex tw:min-w-0 tw:items-center tw:justify-between tw:gap-1.5 tw:sm:flex-col tw:sm:items-start tw:sm:pt-2";

export const WALL_ROW_TITLE_WRAP_CLASS =
  "tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-1.5";

export const WALL_ROW_LABEL_CLASS =
  "tw:m-0 tw:min-w-0 tw:wrap-anywhere tw:text-[0.8125rem] tw:leading-5 tw:font-semibold tw:text-muted-foreground";

export const WALL_ROW_EXPAND_BTN_CLASS =
  "tw:inline-flex tw:flex-none tw:cursor-pointer tw:items-center tw:rounded-md tw:bg-muted tw:px-2 tw:py-0.5 tw:text-xs tw:font-medium tw:text-muted-foreground tw:transition-colors tw:hover:bg-accent tw:hover:text-accent-foreground tw:focus-visible:outline-2 tw:focus-visible:outline-ring";

/** The track shell: data-fade-start/-end marks a side still holding clipped cards, fading that side 2rem. */
export const WALL_ROW_BODY_CLASS =
  "tw:relative tw:isolate tw:min-w-0 tw:[--fade-start:0px] tw:[--fade-end:0px] tw:data-fade-start:[--fade-start:2rem] tw:data-fade-end:[--fade-end:2rem]";

/** The horizontal scroll track: native scrolling + scroll-snap, with the overflowing side faded by a mask (the mask takes only the alpha channel). */
export const WALL_ROW_TRACK_CLASS =
  "tw:m-0 tw:flex tw:list-none tw:gap-(--wall-gap) tw:overflow-x-auto tw:overscroll-x-contain tw:snap-x tw:snap-proximity tw:scroll-px-1 tw:px-1 tw:pt-1.5 tw:pb-2 tw:[scrollbar-width:thin] tw:[mask-image:linear-gradient(to_right,transparent_0,black_var(--fade-start),black_calc(100%_-_var(--fade-end)),transparent_100%)]";

/** The expanded grid track: 2 equal columns on phones, auto-fill poster columns on >=640px. */
export const WALL_ROW_GRID_CLASS =
  "tw:m-0 tw:grid tw:list-none tw:grid-cols-2 tw:gap-(--wall-gap) tw:px-1 tw:pt-1.5 tw:pb-2 tw:sm:grid-cols-[repeat(auto-fill,minmax(var(--wall-card-w),1fr))] tw:[&>li]:w-auto";

/** A card item: fixed poster width in the horizontal track, snapped and aligned by card. */
export const WALL_ROW_ITEM_CLASS = "tw:flex-none tw:w-(--wall-card-w) tw:snap-start";

// The page buttons belong to fine-pointer hover devices only; a button appears on an overflowing side and shows on row hover/focus-within.
const WALL_ROW_NAV_BASE =
  "tw:hidden tw:absolute tw:top-1/2 tw:z-32 tw:size-9 tw:-mt-[1.125rem] tw:items-center tw:justify-center tw:rounded-full tw:border tw:border-border tw:bg-background/90 tw:text-foreground tw:shadow-sm tw:backdrop-blur-[8px] tw:cursor-pointer tw:opacity-0 tw:[@media(hover:hover)_and_(pointer:fine)]:inline-flex tw:group-hover/wall-row:opacity-100 tw:group-focus-within/wall-row:opacity-100 tw:motion-safe:transition-opacity tw:motion-safe:duration-150 tw:motion-safe:ease-[ease]";

export const WALL_ROW_NAV_BACK_CLASS = `${WALL_ROW_NAV_BASE} tw:-left-1`;
export const WALL_ROW_NAV_FORWARD_CLASS = `${WALL_ROW_NAV_BASE} tw:-right-1`;
export const WALL_ROW_NAV_ICON_CLASS = "tw:size-[1.125rem]";
