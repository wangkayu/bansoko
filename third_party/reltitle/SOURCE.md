# Vendored reltitle

- Upstream: `github.com/wangkayu/reltitle`, MIT.
- Commit: `7a2485922c9c9dd9def6142b4b6b0eb4e7f808b3` (`7a24859`), vendored 2026-10-21 (first vendored `ca671af` on 2026-10-11).
- Same author as the project owner. Upstream tracks its MIT `LICENSE` since `2336a6d`;
  the copy here is exactly that commit's tracked files (`git archive`, which includes
  `LICENSE`) plus this file, so the notification requirement MIT states travels with it.
- Contents: do not edit files here; change upstream and refresh.
- Wiring: root `go.mod` has `require github.com/wangkayu/reltitle v0.0.0` and
  `replace github.com/wangkayu/reltitle => ./third_party/reltitle`. The replace directive
  means a build of this tree uses exactly these files, with no module-proxy fetch.

## Refresh

```bash
rm -rf third_party/reltitle/{cmd,configs,pkg,go.mod,go.sum,README.md,.gitignore,LICENSE}
git -C ../reltitle archive <commit> | tar -x -C third_party/reltitle
# update the commit and date above and ImplementationVersion in
# internal/parser/reltitletags; the lexicon digest changes with the configs
go mod tidy && go test ./internal/parser/...
```
