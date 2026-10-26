import type { ReactNode } from "react";

type Block =
  | { kind: "heading"; level: 1 | 2 | 3; text: string }
  | { kind: "paragraph"; text: string }
  | { kind: "list"; ordered: boolean; items: string[] };

const HEADING = /^(#{1,3})\s+(.*)$/;
const BULLET = /^[-*]\s+(.*)$/;
const ORDERED = /^\d+\.\s+(.*)$/;

/** Splits restricted Markdown into blocks. Anything outside headings and lists is plain paragraph text. */
function parseRestrictedMarkdown(source: string): Block[] {
  const blocks: Block[] = [];
  let paragraph: string[] = [];
  let list: { ordered: boolean; items: string[] } | undefined;

  const flushParagraph = () => {
    if (paragraph.length > 0) blocks.push({ kind: "paragraph", text: paragraph.join(" ") });
    paragraph = [];
  };
  const flushList = () => {
    if (list) blocks.push({ kind: "list", ordered: list.ordered, items: list.items });
    list = undefined;
  };

  for (const raw of source.replace(/\r\n?/g, "\n").split("\n")) {
    const line = raw.trim();
    if (line === "") {
      flushParagraph();
      flushList();
      continue;
    }
    const heading = HEADING.exec(line);
    if (heading) {
      flushParagraph();
      flushList();
      blocks.push({ kind: "heading", level: heading[1].length as 1 | 2 | 3, text: heading[2] });
      continue;
    }
    const bullet = BULLET.exec(line);
    const ordered = bullet ? null : ORDERED.exec(line);
    const match = bullet ?? ordered;
    if (match) {
      flushParagraph();
      const isOrdered = !bullet;
      if (list && list.ordered !== isOrdered) flushList();
      list ??= { ordered: isOrdered, items: [] };
      list.items.push(match[1]);
      continue;
    }
    flushList();
    paragraph.push(line);
  }
  flushParagraph();
  flushList();
  return blocks;
}

/**
 * Renders release-note text. Only headings, paragraphs and lists are interpreted; HTML,
 * links, images and every other Markdown construct are shown as literal text.
 */
export function RestrictedMarkdown({ source }: { source: string }): ReactNode {
  const blocks = parseRestrictedMarkdown(source);
  if (blocks.length === 0) return null;
  return (
    <div className="tw:grid tw:gap-1.5 tw:[overflow-wrap:anywhere]">
      {blocks.map((block, index) => {
        switch (block.kind) {
          case "heading":
            return block.level === 1 ? (
              <h3 key={index} className="text-body tw:m-0 tw:font-medium tw:text-foreground">{block.text}</h3>
            ) : block.level === 2 ? (
              <h4 key={index} className="text-body tw:m-0 tw:font-medium tw:text-foreground">{block.text}</h4>
            ) : (
              <h5 key={index} className="text-caption tw:m-0 tw:font-medium tw:text-foreground">{block.text}</h5>
            );
          case "paragraph":
            return <p key={index} className="text-caption tw:m-0">{block.text}</p>;
          case "list": {
            const Tag = block.ordered ? "ol" : "ul";
            return (
              <Tag key={index} className={["text-caption tw:m-0 tw:pl-5", block.ordered ? "tw:list-decimal" : "tw:list-disc"].join(" ")}>
                {block.items.map((item, itemIndex) => (
                  <li key={itemIndex}>{item}</li>
                ))}
              </Tag>
            );
          }
        }
      })}
    </div>
  );
}
