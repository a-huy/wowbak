# Contributing

## Commit messages

Versions and the changelog are generated from commit messages, so the prefix
matters:

```
fix:      a bug fix          -> patch release
feat:     a new capability   -> minor release
feat!:    a breaking change  -> major release
docs:     documentation      -> no release
chore:    tidying, tooling   -> no release
refactor: no behaviour change -> no release
test:     tests only          -> no release
```

Anything else is ignored by the release tooling: it will not appear in the
changelog and will not move the version.

Put a breaking change in the body as well, so it is hard to miss:

```
feat!: store settings as text rather than JSON

BREAKING CHANGE: an existing config.json is no longer read. Run
"wowbak config init" and set install_path again.
```

## Before pushing

```
gofmt -l .    # must print nothing
go vet ./...
go test ./...
./build.sh    # builds the portable folder for every platform
```

CI runs all of these plus a cross-compile of every target.
