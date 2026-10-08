# Documentation

These pages are copied from the original Python project,
[lsaint/aikito](https://github.com/lsaint/aikito) (MIT licensed, upstream commit
`a24df1c`, 2026-10-08), and describe Aikito's concepts, workspace layout and
commands. The Go port aims to behave the same way, so most of the content
applies as written. Start at [index.md](index.md) or the
[getting-started guide](guide.md).

They have not been rewritten for the Go port. Where they differ:

- **[installation.md](installation.md)** describes installing the Python
  package (`uv tool install aikito`, `pipx`, Homebrew). For this port, see
  [Install](../README.md#install) in the top-level README.
- **[python-api.md](python-api.md)** documents the Python package's library
  API. The Go port is a CLI only; its `internal/` packages are not a public API.
- **`aikito web`** (the read-only browser console, mentioned in several pages)
  and multi-machine workspace sync
  ([workspace-portability.md](workspace-portability.md)) are not implemented in
  the Go port yet.
- Some command options are not implemented yet. See
  [Status](../README.md#status) in the top-level README for the current list.

`overrides/`, `stylesheets/` and `assets/` belong to the upstream mkdocs site
theme and are kept so the pages and images render as they do upstream.
