import { useEffect, useState, type ReactNode } from "react";
import { cn } from "../../lib/utils";

export const REVEAL_MS = 200;

/**
 * 展开/收起：行高从 0fr 过渡到 1fr（内容高度未知也能平滑）。收起时内容保留到过渡
 * 结束再卸载；reduced-motion 下没有过渡，直接到终态。
 */
export function Reveal({ open, children }: { open: boolean; children: ReactNode }) {
  const [mounted, setMounted] = useState(open);
  const [shown, setShown] = useState(open);
  useEffect(() => {
    if (open) {
      setMounted(true);
      const frame = window.requestAnimationFrame(() => setShown(true));
      return () => window.cancelAnimationFrame(frame);
    }
    setShown(false);
    const timer = window.setTimeout(() => setMounted(false), REVEAL_MS);
    return () => window.clearTimeout(timer);
  }, [open]);
  if (!mounted) return null;
  return (
    <div
      className={cn(
        "tw:grid tw:transition-[grid-template-rows,opacity] tw:duration-200 tw:ease-out tw:motion-reduce:transition-none",
        shown ? "tw:grid-rows-[1fr] tw:opacity-100" : "tw:grid-rows-[0fr] tw:opacity-0",
      )}
    >
      <div className="tw:min-h-0 tw:overflow-hidden">{children}</div>
    </div>
  );
}
