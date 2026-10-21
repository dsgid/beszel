# DSG build of Beszel

This fork carries DSG patches on top of an upstream release tag. The hub at
beszel.dsg.id runs the image built by `build-and-push.sh`.

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

See the script header for what it needs and what it prints.

## Upgrading to a new upstream release

Rebase the DSG commits onto the new upstream tag, rebuild, and deploy the new
tag. Keep the base on a release tag, not upstream `main`: agents and the hub
database must match a released version. If upstream CI changes how it builds
the web UI before `docker build`, mirror that in the script.
