# Releasing

Cross-platform binary releases via [GoReleaser](https://goreleaser.com).
Releases land as **drafts** so they're invisible to the public until
someone clicks Publish in the GitHub UI.

## One-time setup

Local-only (for `make release-snapshot` / `make release-test`):

```bash
brew install goreleaser
```

No secrets needed for the in-repo flow — GitHub Actions uses the
built-in `GITHUB_TOKEN`.

## Cutting a release

1. Make sure `main` is green and contains the changes you want.
2. Pick a version: `vMAJOR.MINOR.PATCH` (semver, leading `v`).
3. Tag and push:

   ```bash
   git tag v0.3.12
   git push origin v0.3.12
   ```

4. GitHub Actions kicks off `.github/workflows/release.yml`. It:
   - Builds the React dashboard.
   - Runs `goreleaser release --clean`.
   - Cross-compiles for `darwin/{amd64,arm64}`, `linux/{amd64,arm64}`,
     and `windows/amd64`.
   - Creates a **draft** release with the binaries attached.
5. Open the draft at <https://github.com/aeolus-labs/aeolus/releases>.
   Review the artifacts and the auto-generated changelog. Edit notes if
   you want. Leave as draft for private testing; **click Publish** when
   ready to make it public.

## Testing without tagging

```bash
make release-test       # validate .goreleaser.yaml syntax
make release-snapshot   # build all platforms into ./dist/ — no publish
```

`make release-snapshot` is the closest local equivalent to what CI runs.
The resulting `dist/aeolus_0.0.0-next_<os>_<arch>.tar.gz` archives are
real binaries; you can extract one and run it.

## Adding the Homebrew tap (deferred)

When ready, create a private (or public) repo named
`aeolus-labs/homebrew-tap` and:

1. Generate a personal access token with `repo` scope on the tap repo.
2. Add it as a secret on this repo: `HOMEBREW_TAP_GITHUB_TOKEN`.
3. Uncomment the `brews:` block in `.goreleaser.yaml`.
4. Add `HOMEBREW_TAP_GITHUB_TOKEN: ${{ secrets.HOMEBREW_TAP_GITHUB_TOKEN }}`
   to the env of the release workflow.

After the next release, users can `brew install aeolus-labs/tap/aeolus`.

## Version metadata in the binary

The `main.version`, `main.commit`, `main.date` vars are injected via
`-ldflags` at build time by GoReleaser. Local `go build` and `make build`
both leave them at their `"dev"` / empty defaults. Check with:

```bash
./aeolus --version
```
