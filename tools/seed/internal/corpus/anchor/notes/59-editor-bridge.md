---
title: Editor bridge notes
author: storage
editors: owner
tags: frontend, wasm, typescript
created: 80
---
The browser editor counts positions in UTF-16 code units; the CRDT counts code points. The WebAssembly bridge converts at the boundary and nowhere else.

```typescript
export function utf16ToCodePoint(text: string, utf16Offset: number): number {
  let codePoints = 0;
  for (let i = 0; i < utf16Offset; i++) {
    const unit = text.charCodeAt(i);
    if (unit >= 0xd800 && unit <= 0xdbff) {
      i++;
    }
    codePoints++;
  }
  return codePoints;
}
```

- An emoji like 👩‍💻 is three code points and five UTF-16 units.
- A cursor between the halves of a surrogate pair is snapped to the next code point.

Design background: [[Offline sync design]].
