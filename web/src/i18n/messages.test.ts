import { describe, expect, test } from "vitest";
import { en, enFragments, zh, zhFragments } from "./messages";

describe("translation dictionaries", () => {
  for (const [language, fragments, dictionary] of [
    ["en", enFragments, en],
    ["zh-CN", zhFragments, zh]
  ] as const) {
    test(`${language} merges every fragment key exactly once`, () => {
      const keys = fragments.flatMap(fragment => Object.keys(fragment));
      expect(keys).toHaveLength(Object.keys(dictionary).length);
      expect(Object.keys(dictionary).sort()).toEqual([...keys].sort());
    });
  }

  test("both languages contain the same keys", () => {
    expect(Object.keys(zh).sort()).toEqual(Object.keys(en).sort());
  });
});
