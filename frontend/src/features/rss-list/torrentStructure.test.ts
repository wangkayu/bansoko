import { describe, expect, it } from "vitest";
import { displayCells, formatStructureBytes, torrentStructureEntries } from "./torrentStructure";

describe("torrentStructureEntries", () => {
  it("把根下路径归并成一级条目：带 / 的是目录（递归大小 + 文件数），不带的是根级文件", () => {
    expect(
      torrentStructureEntries([
        { path: "Show/Season 1/Show - 01.mkv", size_bytes: 2048 },
        { path: "Show/Season 1/Show - 02.mkv", size_bytes: 1024 },
        { path: "readme.txt", size_bytes: 12 },
        { path: "cover.jpg", size_bytes: 100 },
      ]),
    ).toEqual([
      { name: "Show", directory: true, sizeBytes: 3072, fileCount: 2 },
      { name: "cover.jpg", directory: false, sizeBytes: 100, fileCount: 1 },
      { name: "readme.txt", directory: false, sizeBytes: 12, fileCount: 1 },
    ]);
  });
});

describe("formatStructureBytes", () => {
  it("1 KiB 以下按字节，以上按二进制单位保留一位小数", () => {
    expect(formatStructureBytes(0)).toBe("0 B");
    expect(formatStructureBytes(1023)).toBe("1023 B");
    expect(formatStructureBytes(1024)).toBe("1.0 KiB");
    expect(formatStructureBytes(1536)).toBe("1.5 KiB");
    expect(formatStructureBytes(5 * 1024 * 1024)).toBe("5.0 MiB");
    expect(formatStructureBytes(3 * 1024 * 1024 * 1024)).toBe("3.0 GiB");
  });
});

describe("displayCells", () => {
  it("中日韩与全角字符占两格，其余占一格", () => {
    expect(displayCells("Show/")).toBe(5);
    expect(displayCells("中文/")).toBe(5);
    expect(displayCells("Ａ１")).toBe(4);
  });
});
