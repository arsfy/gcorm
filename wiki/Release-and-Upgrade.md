# Release And Upgrade

This page explains how GCORM CLI versions are built, checked, and upgraded.

## Version Output

```sh
gco version
```

The command prints the local CLI version. It does not upgrade the binary.

## Upgrade Command

```sh
gco upgrade
```

`gco upgrade` checks the latest available release and upgrades the CLI only when
the current binary was installed with:

```sh
go install github.com/arsfy/gcorm/cmd/gco@version
```

The upgrade path uses:

```sh
go install github.com/arsfy/gcorm/cmd/gco@latest
```

## Manual Binary Installs

If you installed `gco` from a release archive, update manually:

1. Download the new archive from GitHub Releases.
2. Replace the old `gco` binary.
3. Run `gco version`.

Manual binaries are not replaced by `gco upgrade`.

## Source Builds

For local development:

```sh
go build ./cmd/gco
```

Release builds can inject a version with linker flags:

```sh
go build -ldflags "-X main.Version=v0.1.0" ./cmd/gco
```

When installed through `go install`, Go build metadata can also provide module
version information.

## Update Checks

GCORM does not need to check for updates on every normal command. The intended
update workflow is explicit:

```sh
gco upgrade
```

This keeps normal CLI commands predictable and avoids unexpected network checks
during validation, generation, or database workflows.

