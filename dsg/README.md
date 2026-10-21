# DSG build of Beszel

This fork carries DSG patches and merges upstream `main`. The hub at
beszel.dsg.id runs an image built by `build-and-push.sh`. The deployed commit,
digest and rollback are recorded in dsgid/infrastructure at
`configs/suite/suite-beszel-hub-ezqlmd/README.md`.

## Patches

- Custom system order. Drag a card or row by its grip on All Systems to reorder
  it. The order is saved per user in `user_settings.settings.systemOrder`, so it
  needs no schema change and stock Beszel ignores it. View, then Sort By, then
  Custom brings the saved order back after sorting by a column.

## Build and push

```sh
git clone --branch <branch> https://github.com/dsgid/beszel /tmp/beszel-build
/tmp/beszel-build/dsg/build-and-push.sh
```

See the script header for what it needs and what it prints. If upstream CI
changes how it builds the web UI before `docker build`, mirror that in the
script.

## Before deploying a newer commit

Upstream `main` runs ahead of the latest release and can add PocketBase
migrations while `beszel.go` still reports the released version. Compare
`internal/migrations` against the deployed commit. If it changed, back up the
hub data first and treat rollback as restoring that backup, not as swapping the
image back.
