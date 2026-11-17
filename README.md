# snapcompact-go

Pure Go, provider-neutral mechanical history projections and PNG rendering.
Developed for WebMux issues [#638](https://github.com/mzdluo123/webmux/issues/638) and [#640](https://github.com/mzdluo123/webmux/issues/640).

```sh
go get github.com/mzdluo123/snapcompact-go
CGO_ENABLED=0 go test ./...
go test -race ./...
go test -run Example ./...
go test -run '^$' -bench . -benchmem
```

Use `Project(ctx, Input, DefaultPreprocessConfig())` to make a projection.
For mixed text/image archives, call `NewArchiveRenderer()` then
`Compact(ctx, projection, ArchiveConfig{Model: "gpt-6-astra", MaxPages: 8})`.
Short histories stay text; longer histories retain text edges and image the middle.
The existing `NewRenderer`/`Render` and `Plan` APIs remain available unchanged.
This package does not send requests, parse agent files, or create forks.

Inputs retain stable IDs, order, roles, tool correlations and status.
Tool output, argument and call-body rune limits use deterministic head/tail
retention. Source ranges refer to original runes; image ranges to projection
text. CurrentTask stays separate and unchanged. Callers retain original text;
whitespace normalization is lossy. Coverage does not prove model understanding.

Archive rendering uses the public-domain X.Org 8x13 bitmap grid with Noto CJK
fallback, grapheme-safe boundaries, and bounded pages, bytes and estimated tokens.
Missing image glyphs are explicit errors; text-only glyphs do not require font coverage.
Complex script shaping, bidi, emoji sequences and combining anchor placement are not verified.
Production requires neither Node, Bun, Python nor a Rust executable.

Independent Go code is [MIT licensed](LICENSE). Noto uses [SIL OFL 1.1](fonts/LICENSE);
the 8x13 bitmap is public domain. [Fixed source notices](fonts/NOTICE.txt) and
[dependency notices](THIRD_PARTY_NOTICES.txt) accompany the assets. Fixtures are synthetic.
[Reference notes](REFERENCE.md) document fixed commits and comparison limits.
No measured token savings, OCR accuracy or model task-success claims are made.

The initial release exports WebMux's snapcompact package as a standalone module.

[API and limits](API.md) · [Official compatibility suite](COMPATIBILITY.md)
