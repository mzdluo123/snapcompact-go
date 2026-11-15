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

Use `Project(ctx, Input, DefaultPreprocessConfig())`, then `NewRenderer()` and
`Render(ctx, projection, DefaultRenderConfig())`. See [examples](example_test.go).
`Plan` checks independently configured text tokens, image tokens, image count,
actual encoded request bytes and context capacity. Unknown stays unknown.
This package does not send requests, parse agent files, or create forks.

Inputs retain stable IDs, order, roles, tool correlations and status.
Tool output, argument and call-body rune limits use deterministic head/tail
retention. Source ranges refer to original runes; image ranges to projection
text. CurrentTask stays separate and unchanged. Callers retain original text;
whitespace normalization is lossy. Coverage does not prove model understanding.

Rendering embeds Noto CJK, uses grapheme wrapping and bounded pages/pixels/memory,
and explicitly reports missing glyphs. Complex script shaping, bidi, emoji
sequences and precise combining anchor placement are not verified.
Production requires neither Node, Bun, Python nor a Rust executable.

Independent Go code is [MIT licensed](LICENSE). The font uses [SIL OFL 1.1](fonts/LICENSE)
with [fixed source notices](fonts/NOTICE.txt). [Dependency notices](THIRD_PARTY_NOTICES.txt)
include BSD-3-Clause and MIT terms. Fixtures are synthetic, not user data.
[Reference notes](REFERENCE.md) document fixed commits and comparison limits.
No measured token savings, OCR accuracy or model task-success claims are made.

The initial release exports WebMux's snapcompact package as a standalone module.
