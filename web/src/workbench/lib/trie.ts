// 찾은 경로의 주소 구조(사이트 맵) 트리. 원본 trie · under · fresh + smNode 의 자식 정렬.

export interface TrieNode<T> {
  /** 이 마디의 경로 조각 (루트는 "") */
  seg: string;
  /** 루트부터의 경로 ("/api/orders"). 루트는 "/". 펼침 상태(smOpen)의 키. */
  key: string;
  /** 자식 마디 (넣은 순서 유지) */
  kids: Map<string, TrieNode<T>>;
  /** 정확히 이 경로에 붙은 항목들 (같은 주소의 여러 메서드) */
  ms: T[];
}

/** 원본 `trie(list)`: 경로를 "/" 로 쪼개 트리로 묶는다. */
export function trie<T extends { p: string }>(list: readonly T[]): TrieNode<T> {
  const root: TrieNode<T> = { seg: "", key: "/", kids: new Map(), ms: [] };
  for (const f of list) {
    let node = root;
    let key = "";
    for (const sg of f.p === "/" ? [] : f.p.slice(1).split("/")) {
      key += `/${sg}`;
      let next = node.kids.get(sg);
      if (!next) {
        next = { seg: sg, key, kids: new Map(), ms: [] };
        node.kids.set(sg, next);
      }
      node = next;
    }
    node.ms.push(f);
  }
  return root;
}

/** 원본 `under(n)`: 이 마디와 그 아래에 붙은 항목 수. */
export function under<T>(n: TrieNode<T>): number {
  let c = n.ms.length;
  for (const k of n.kids.values()) c += under(k);
  return c;
}

/** 원본 `fresh(n)`: 이 마디나 그 아래에 방금 찾은 항목이 있는가. */
export function fresh<T extends { fresh?: boolean }>(n: TrieNode<T>): boolean {
  if (n.ms.some((f) => f.fresh)) return true;
  for (const k of n.kids.values()) if (fresh(k)) return true;
  return false;
}

/** 원본 smNode 의 자식 정렬: `{param}` 조각은 뒤로, 나머지는 글자 순. */
export function sortedKids<T>(n: TrieNode<T>): TrieNode<T>[] {
  return [...n.kids.keys()]
    .sort((x, y) => {
      const bx = x.charAt(0) === "{";
      const by = y.charAt(0) === "{";
      if (bx !== by) return bx ? 1 : -1;
      if (x === y) return 0;
      return x < y ? -1 : 1;
    })
    .map((k) => n.kids.get(k) as TrieNode<T>);
}
