import { createHash } from "node:crypto";
import { describe, expect, it } from "vitest";
import { sha256Hex } from "./sha256";

const encode = (text: string) => new TextEncoder().encode(text);

describe("sha256Hex", () => {
  it("matches the FIPS 180-2 vectors", () => {
    expect(sha256Hex(encode(""))).toBe("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
    expect(sha256Hex(encode("abc"))).toBe("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
    expect(sha256Hex(encode("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq"))).toBe("248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1");
  });

  it("agrees with node crypto across block boundaries and multibyte input", () => {
    for (const length of [1, 55, 56, 63, 64, 65, 119, 120, 1000]) {
      const text = "业务x".repeat(length);
      expect(sha256Hex(encode(text))).toBe(createHash("sha256").update(text).digest("hex"));
    }
  });
});
