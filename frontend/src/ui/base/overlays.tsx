import * as React from "react";
import { Menu as BaseMenu } from "@base-ui/react/menu";
import { Popover as BasePopover } from "@base-ui/react/popover";
import { Tooltip as BaseTooltip } from "@base-ui/react/tooltip";
import { Check, ChevronRight } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./controls";
import { usePopupZIndex } from "./layers";
import { t } from "../../lib/i18n";
import "./base.css";

// Floating overlays anchored to a trigger: popover, dropdown menu, tooltip.
// They stack POPUP_OFFSET above whatever layer contains them (layers.ts), so a
// menu inside a nested dialog still renders above that dialog. Triggers are
// passed as an element and rendered through Base UI's `render` prop, so the
// caller keeps its own button markup (it must forward ref and props).

type Side = "top" | "bottom" | "left" | "right";
type Align = "start" | "center" | "end";

const surface =
  "tw:rounded-control tw:border tw:border-border tw:bg-popover tw:text-popover-foreground tw:shadow-2 tw:outline-none " +
  "tw:origin-(--transform-origin) tw:transition-[opacity,scale] tw:duration-150 tw:data-starting-style:opacity-0 tw:data-starting-style:scale-[0.98] tw:data-ending-style:opacity-0";

export function Popover({
  trigger,
  children,
  title,
  open,
  onOpenChange,
  side = "bottom",
  align = "center",
  className,
}: {
  trigger: React.ReactElement;
  children: React.ReactNode;
  title?: React.ReactNode;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  side?: Side;
  align?: Align;
  className?: string;
}) {
  const zIndex = usePopupZIndex();
  return (
    <BasePopover.Root open={open} onOpenChange={onOpenChange ? (next) => onOpenChange(next) : undefined}>
      <BasePopover.Trigger render={trigger} />
      <BasePopover.Portal>
        <BasePopover.Positioner side={side} align={align} sideOffset={6} style={{ zIndex }} className="tw:outline-none">
          <BasePopover.Popup className={cn(surface, "tw:max-w-[min(360px,calc(100vw-2rem))] tw:p-3 tw:text-sm", className)}>
            {title ? <BasePopover.Title className="tw:m-0 tw:mb-2 tw:text-sm tw:font-semibold">{title}</BasePopover.Title> : null}
            {children}
          </BasePopover.Popup>
        </BasePopover.Positioner>
      </BasePopover.Portal>
    </BasePopover.Root>
  );
}

/**
 * Confirmation anchored to its trigger. For a
 * destructive action next to the thing it affects; use a blocking Dialog when
 * the consequence needs a full stop. Open state is controlled by the caller.
 */
export function Popconfirm({
  trigger,
  title,
  description,
  confirmLabel,
  cancelLabel = t("ui.cancel"),
  danger = false,
  confirmLoading = false,
  open,
  onOpenChange,
  onConfirm,
  align = "end",
}: {
  trigger: React.ReactElement;
  title: React.ReactNode;
  description?: React.ReactNode;
  confirmLabel: string;
  cancelLabel?: string;
  danger?: boolean;
  confirmLoading?: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
  align?: Align;
}) {
  const zIndex = usePopupZIndex();
  return (
    <BasePopover.Root open={open} onOpenChange={(next) => onOpenChange(next)}>
      <BasePopover.Trigger render={trigger} />
      <BasePopover.Portal>
        <BasePopover.Positioner side="bottom" align={align} sideOffset={6} style={{ zIndex }} className="tw:outline-none">
          <BasePopover.Popup className={cn(surface, "tw:grid tw:max-w-[min(320px,calc(100vw-2rem))] tw:gap-3 tw:p-3 tw:text-sm")}>
            <div className="tw:grid tw:gap-1">
              <BasePopover.Title className="tw:m-0 tw:text-sm tw:font-semibold">{title}</BasePopover.Title>
              {description ? (
                <BasePopover.Description className="tw:m-0 tw:text-xs tw:text-muted-foreground">{description}</BasePopover.Description>
              ) : null}
            </div>
            <div className="tw:flex tw:justify-end tw:gap-2">
              <Button size="sm" onClick={() => onOpenChange(false)}>
                {cancelLabel}
              </Button>
              <Button size="sm" variant={danger ? "danger" : "primary"} loading={confirmLoading} onClick={onConfirm}>
                {confirmLabel}
              </Button>
            </div>
          </BasePopover.Popup>
        </BasePopover.Positioner>
      </BasePopover.Portal>
    </BasePopover.Root>
  );
}

export type MenuEntry =
  | { type?: "item"; key: string; label: React.ReactNode; icon?: React.ReactNode; danger?: boolean; disabled?: boolean; onSelect: () => void }
  | { type: "radio"; key: string; value: string; onValueChange: (next: string) => void; options: { value: string; label: React.ReactNode; icon?: React.ReactNode }[] }
  | { type: "submenu"; key: string; label: React.ReactNode; icon?: React.ReactNode; items: MenuEntry[] }
  | { type: "divider"; key: string };

const menuRow =
  "tw:flex tw:cursor-default tw:items-center tw:gap-2 tw:rounded-md tw:px-2 tw:py-1.5 tw:outline-none tw:select-none " +
  "tw:data-highlighted:bg-accent tw:data-disabled:cursor-not-allowed tw:data-disabled:opacity-50";
const menuPopup = cn(surface, "tw:min-w-40 tw:p-1 tw:text-sm");

function MenuIcon({ icon }: { icon: React.ReactNode }) {
  return <span className="tw:grid tw:size-4 tw:flex-none tw:place-items-center tw:text-muted-foreground">{icon}</span>;
}

function renderMenuEntries(items: MenuEntry[], zIndex: number): React.ReactNode {
  return items.map((entry) => {
    if (entry.type === "divider") {
      return <BaseMenu.Separator key={entry.key} className="tw:mx-1 tw:my-1 tw:h-px tw:bg-border" />;
    }
    if (entry.type === "radio") {
      return (
        <BaseMenu.RadioGroup key={entry.key} value={entry.value} onValueChange={(next) => entry.onValueChange(String(next))}>
          {entry.options.map((option) => (
            <BaseMenu.RadioItem key={option.value} value={option.value} closeOnClick className={cn(menuRow, "tw:text-foreground")}>
              <MenuIcon icon={option.icon} />
              <span className="tw:min-w-0 tw:flex-1">{option.label}</span>
              <BaseMenu.RadioItemIndicator className="tw:flex tw:text-primary">
                <Check className="tw:size-3.5" />
              </BaseMenu.RadioItemIndicator>
            </BaseMenu.RadioItem>
          ))}
        </BaseMenu.RadioGroup>
      );
    }
    if (entry.type === "submenu") {
      return (
        <BaseMenu.SubmenuRoot key={entry.key}>
          <BaseMenu.SubmenuTrigger className={cn(menuRow, "tw:text-foreground")}>
            <MenuIcon icon={entry.icon} />
            <span className="tw:min-w-0 tw:flex-1">{entry.label}</span>
            <ChevronRight className="tw:size-3.5 tw:text-muted-foreground" aria-hidden="true" />
          </BaseMenu.SubmenuTrigger>
          <BaseMenu.Portal>
            <BaseMenu.Positioner sideOffset={4} style={{ zIndex }} className="tw:outline-none">
              <BaseMenu.Popup className={menuPopup}>{renderMenuEntries(entry.items, zIndex)}</BaseMenu.Popup>
            </BaseMenu.Positioner>
          </BaseMenu.Portal>
        </BaseMenu.SubmenuRoot>
      );
    }
    return (
      <BaseMenu.Item
        key={entry.key}
        disabled={entry.disabled}
        onClick={entry.onSelect}
        className={cn(menuRow, entry.danger ? "tw:text-destructive" : "tw:text-foreground")}
      >
        {entry.icon ? <MenuIcon icon={entry.icon} /> : null}
        <span className="tw:min-w-0 tw:flex-1">{entry.label}</span>
      </BaseMenu.Item>
    );
  });
}

/** Dropdown menu: action items, one-of-many radio groups, submenus and dividers. */
export function DropdownMenu({
  trigger,
  items,
  open,
  onOpenChange,
  align = "end",
  className,
}: {
  trigger: React.ReactElement;
  items: MenuEntry[];
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  align?: Align;
  className?: string;
}) {
  const zIndex = usePopupZIndex();
  return (
    <BaseMenu.Root open={open} onOpenChange={onOpenChange ? (next) => onOpenChange(next) : undefined}>
      <BaseMenu.Trigger render={trigger} />
      <BaseMenu.Portal>
        <BaseMenu.Positioner side="bottom" align={align} sideOffset={6} style={{ zIndex }} className="tw:outline-none">
          <BaseMenu.Popup className={cn(menuPopup, className)}>{renderMenuEntries(items, zIndex)}</BaseMenu.Popup>
        </BaseMenu.Positioner>
      </BaseMenu.Portal>
    </BaseMenu.Root>
  );
}

/** Hover/focus hint. Never the only carrier of information a user needs. */
export function Tooltip({
  content,
  children,
  side = "top",
  delay = 400,
}: {
  content: React.ReactNode;
  children: React.ReactElement;
  side?: Side;
  delay?: number;
}) {
  const zIndex = usePopupZIndex();
  return (
    <BaseTooltip.Root>
      <BaseTooltip.Trigger render={children} delay={delay} />
      <BaseTooltip.Portal>
        <BaseTooltip.Positioner side={side} sideOffset={6} style={{ zIndex }}>
          <BaseTooltip.Popup className={cn(surface, "tw:max-w-72 tw:px-2 tw:py-1 tw:text-xs")}>{content}</BaseTooltip.Popup>
        </BaseTooltip.Positioner>
      </BaseTooltip.Portal>
    </BaseTooltip.Root>
  );
}
