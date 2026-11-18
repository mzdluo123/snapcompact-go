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

Use `CompactOMP(ctx, messages, options)` for oh-my-pi Snapcompact 18.22.2
archives. It translates the OMP serializer, normalization, model-aware frame
selection, pagination, and bitmap rendering into Go. `archive.Blocks()` yields
ordered text and PNG frames. `archive.Summary` and `archive.HistoryText()` expose
the reference summary and retained normalized transcript. The caller supplies
the messages, model/API, and any history limits. Build, tests, and runtime use
the Go toolchain.

`Project`, `NewRenderer`, `Plan`, and `NewArchiveRenderer` remain available
for callers that use source-addressable mechanical projections.

The reference tests compare summary and transcript, text edges, frame order,
image-token estimates, and decoded pixels. PNG encoding uses Go's standard
library. [Provenance](PROVENANCE.txt) pins the upstream source and assets.
[Dependency notices](THIRD_PARTY_NOTICES.txt) cover the upstream and bundled
fonts. Fixtures are synthetic.

The initial release exports WebMux's snapcompact package as a standalone module.

[API and limits](API.md) · [Official compatibility suite](COMPATIBILITY.md)
