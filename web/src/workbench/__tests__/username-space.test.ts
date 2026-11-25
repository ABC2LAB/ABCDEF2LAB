// 아이디 칸 공백 막기(사용자 11/25). 서버 TrimSpace 와 같은 공백 문자를 모두 지우고 커서 자리를 지킨다.

import { describe, expect, it } from "vitest";

import { NO_SPACE_USERNAME, stripUsernameWhitespace, validAccountCredential } from "../live/accounts";

function input(value: string, caret = value.length): HTMLInputElement {
  const el = document.createElement("input");
  el.value = value;
  el.setSelectionRange(caret, caret);
  return el;
}

describe("stripUsernameWhitespace", () => {
  it("앞 · 뒤 · 가운데 공백(탭 · 줄바꿈 · 전각 · NBSP 포함)을 모두 지운다", () => {
    const el = input(" a\tb\nc\u3000d\u00a0e ");
    stripUsernameWhitespace(el);
    expect(el.value).toBe("abcde");
    expect(validAccountCredential(el.value, "pw")).toBe(true);
  });

  it("커서 앞에서 지운 만큼 커서를 당긴다(가운데 입력 중에도 커서가 튀지 않음)", () => {
    const el = input("ab c d", 4); // 'ab c|' 뒤
    stripUsernameWhitespace(el);
    expect(el.value).toBe("abcd");
    expect(el.selectionStart).toBe(3);
  });

  it("공백이 없으면 값 · 커서를 건드리지 않는다", () => {
    const el = input("user-1", 2);
    stripUsernameWhitespace(el);
    expect(el.value).toBe("user-1");
    expect(el.selectionStart).toBe(2);
  });

  it("한글 조합 중에는 건드리지 않고 조합이 끝나면 지운다", () => {
    const el = input("가 나");
    NO_SPACE_USERNAME.onInput({ currentTarget: el, nativeEvent: { isComposing: true } as unknown as Event });
    expect(el.value).toBe("가 나");
    NO_SPACE_USERNAME.onCompositionEnd({ currentTarget: el });
    expect(el.value).toBe("가나");
  });
});
