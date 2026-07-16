# Spor

![GitHub release (latest SemVer)](https://img.shields.io/github/v/release/emprcl/spor) ![GitHub Workflow Status](https://img.shields.io/github/actions/workflow/status/emprcl/spor/build.yml)

:notebook: **[User Manual](https://empr.cl/spor/)**

Spor is a simple, automatic versioning tool for exploratory work, for when
you're trying things out, backtracking, and changing direction without a plan
mapped out in advance, and you don't want to lose where you've been.

**_Spor is a work-in-progress. The command surface and on-disk format should not
be considered stable until 1.0._**

_Feel free to [open an issue](https://github.com/emprcl/spor/issues/new)._

<p align="center">
  <img src="/docs/screenshot.png" width="50%">
</p>

It works differently from traditional version control like
[Git](https://git-scm.com/). There are no commits to write, nothing to stage, no
branches to manage. Instead, you leave `spor watch` running while you work: it
automatically saves a snapshot every time something changes (recording stops
when you stop watching). You can jump back to any past moment, pick up from
there, and go a different way. You can think of it as infinite undo for your
whole project.

Everything is automatic and local. Spor records your history as an immutable
graph of snapshots as you work, storing each unique file once (deduplicated and
compressed). Returning to any moment is fast, and history is only ever removed
when you explicitly ask for it.

Spor was built with creative coding workflows in mind first, but we think the
same shape, explore, backtrack, don't lose anything, shows up in a lot of other
work too. We're looking for people to try it in their own workflow and
tell us where it breaks, where it's confusing, and where it doesn't fit. If
that's you, we'd love your feedback.

A few workflows it might fit well:
- **Creative coding**: generative art, shaders, sound patches, experimenting with
  parameters and reverting fast when a direction doesn't pan out.
- **Design & prototyping**: iterating on a layout or concept where you want to
  freely revisit earlier directions.
- **Writing**: drafts that go through structural rewrites, letting you recover a
  scrapped version without keeping fifteen file copies.
- **Data & research notebooks**: exploratory analysis where you want to backtrack
  after a dead-end path.

## Installation

### Supported platforms

| OS      | Architectures                        |
| ------- | ------------------------------------ |
| Linux   | x86-64 (amd64), arm64                |
| macOS   | Intel (amd64), Apple Silicon (arm64) |
| Windows | x86-64 (amd64), arm64                |

### Quick-install

On **linux** or **macOS**, you can run this quick-install bash script:
```sh
curl -sSL empr.cl/get/spor | bash
```

### Packages

#### Homebrew (macOS & Linux)

```sh
brew tap emprcl/tap
brew trust emprcl/tap
brew install spor
```

### Manual installation

#### Linux & macOS

[Download the last release](https://github.com/emprcl/spor/releases) for your platform.

Then:
```sh
# Extract files
mkdir -p spor && tar -zxvf spor_VERSION_PLATFORM.tar.gz -C spor
cd spor

# Check it runs
./spor --version

# Optionally, move it onto your PATH
sudo mv spor /usr/local/bin/
```

#### Windows

> _Spor's history view uses colors and box-drawing characters, so a terminal like
> Windows Terminal with a good monospace font renders it best._

Unzip the last [windows release](https://github.com/emprcl/spor/releases) and, in
the same directory, run:
```winbatch
; Check it runs
.\spor.exe --version
```

### Build it yourself

You'll need [go 1.25](https://go.dev/dl/) minimum. Although you should be able to
build it for either **linux**, **macOS** or **Windows**, it has only been tested
on **linux**.

```sh
# Native build
go build -o spor ./cmd/spor

# Cross-compile with GOOS / GOARCH, e.g.
GOOS=linux   GOARCH=arm64 go build -o spor ./cmd/spor
GOOS=darwin  GOARCH=arm64 go build -o spor ./cmd/spor
GOOS=windows GOARCH=amd64 go build -o spor.exe ./cmd/spor
```

## Usage

Run any command inside your project directory. There is nothing to set up: the
first snapshot creates Spor's store automatically, and every command below
just works once you're inside a tracked project.

### Walkthrough

Say you're starting work on a project. `cd` into it and leave the watcher
running while you work:

```sh
cd my-project
spor watch
```

That's it, there's nothing to configure. Every time you save a file, Spor
waits for things to settle and records a snapshot automatically. Leave it
running in a terminal off to the side; `spor watch` shows the history
repainting live as new snapshots land, with `@` marking where you are.

You try something that seems worth being able to get back to easily later, so
you name it from another terminal:

```sh
spor label @ before-refactor
```

You keep iterating. An hour later you've gone down a path that isn't working.
Rather than manually undoing your edits, just ask Spor for the history:

```sh
spor log
```

`spor log` shows every snapshot, newest first, with your named ones called
out. You spot `before-refactor` a bit further back and jump straight to it:

```sh
spor go before-refactor
```

Your files are instantly restored to exactly how they were at that point;
whatever you hadn't snapshotted yet was recorded first, so nothing is lost,
and you can always `spor go` back to where you were. From here you branch off
in a new direction. Curious what actually changed between the two?

```sh
spor diff before-refactor
```

Maybe there's one file from the abandoned attempt you still want, without
pulling back everything else:

```sh
spor pick @~4 config.yaml
```

If you took a wrong turn, `spor undo` (and `spor redo`) step you back and
forth one snapshot at a time, no `<ref>` needed. And once a direction is
clearly done, `spor drop <ref>` permanently deletes it and everything after
it, `spor trim <ref>` throws away everything before a point,
`spor fold <a> <b>` squashes a noisy run of snapshots into one, and
`spor thin` collapses every linear run at once, keeping only your tips, branch
points, and named snapshots, if you want to tidy up before sharing. If you ever
want a clean slate, `spor forget` wipes Spor's history for the project, keeping
your files exactly as they are.

That's the whole workflow: watch, work, and reach back into history whenever
you need to. See the full command reference below, or `spor --help` /
`spor <command> --help` for the same thing from your terminal.

### Commands

**Common**

| Command | Effect |
|---|---|
| `spor watch` | watch the project and snapshot it automatically as you work (Ctrl+C to stop) |
| `spor snap [-l <name>]` | save one snapshot by hand, optionally naming it (only needed when `watch` isn't running) |
| `spor log` | show the project history, newest first, with `@` marking where you are |
| `spor undo [n]` | step back `n` snapshots (default 1); reversible with `redo` |
| `spor redo [n]` | step forward `n` snapshots (default 1) after an `undo` |
| `spor go <ref>` | jump the project back (or forward) to any snapshot |
| `spor pick <ref> <path>` | bring back one file or directory from a past snapshot, leaving everything else alone |

**Naming & inspecting**

| Command | Effect |
|---|---|
| `spor label <ref> <name>` | name a snapshot so you can refer to it later |
| `spor label -d <name>` | remove a label |
| `spor label` | list every label and the snapshot it names |
| `spor diff <ref> [<ref>]` | show what changed from `<ref>` to `@`, or between two snapshots |
| `spor status` | project path, whether a watcher is running, history size, store size, and where `@` sits |

**History editing** (destructive, and confirms first unless `-y` is given)

| Command | Effect |
|---|---|
| `spor drop <ref>` | permanently delete a snapshot and everything after it |
| `spor trim <ref>` | permanently drop everything before a snapshot, keeping it and what follows |
| `spor fold <a> <b>` | squash the run of snapshots from `a` to `b` into one |
| `spor thin` | collapse linear runs across the whole history, keeping only tips, branch points, and named snapshots |

**Starting over**

| Command | Effect |
|---|---|
| `spor forget` | delete all of Spor's history for the project (your files are kept) |

**Maintenance**

| Command | Effect |
|---|---|
| `spor verify` | check the stored history for corruption |
| `spor gc` | reclaim disk space from data no longer referenced by any snapshot |

A `<ref>` can be `@` (the current snapshot), `@~n` (`n` snapshots back), a
duration like `2h` or `3d`, a snapshot id (or a short prefix of one), or a
label. Run `spor <command> --help` for the full details and more examples.

### Ignoring files

`.spor/`, spor's own store, is always excluded. On top of that, a few
common, high-churn or huge directories are ignored by default, so a first
snapshot never sweeps them in: `.git/`, editor/OS temp files (`*.tmp`, `*~`,
`*.swp`, `*.swo`, `.DS_Store`), and directories like `node_modules/`,
`build/`, `dist/`, `target/`, `__pycache__/`, and `.venv/`.

To exclude anything else, add a `.sporignore` file at the project root, using
the same syntax as `.gitignore` (globs, `**`, directory-only `foo/`, `#`
comments, and `!` negation). It's applied on top of the defaults, so you can
re-include one with a negation, e.g. `!build/` if that's where your sources
actually live. `.sporignore` is itself tracked, like `.gitignore`, and spor
never creates it: it's entirely opt-in.

## Documentation

Check the [official website](https://empr.cl/spor/).
See the [Design Specification](docs/design-spec.md) for the full design and the
mental model behind Spor.

## Contributing

Contributions are very welcome! Whether it's code,
documentation, bug reports, examples, or just ideas, you're encouraged to join
in.

Just be kind, inclusive, and patient. We're all here to learn and build
something cool together.

How to contribute:
  1) [Open an issue](https://github.com/emprcl/spor/issues) to report a bug or
     suggest an enhancement. **Please check if one already exists on the same
     topic first.**
  2) Open a [Pull Request](https://github.com/emprcl/spor/pulls). Please keep
     it small and focused.

You can also contribute by sharing that you used Spor on
social media, or anywhere else. We'd love to see it!

## Acknowledgments

Spor uses a few awesome packages:
 - [spf13/cobra](https://github.com/spf13/cobra) for the CLI
 - [charmbracelet/fang](https://github.com/charmbracelet/fang) for CLI styling and help
 - [charm.land/lipgloss](https://github.com/charmbracelet/lipgloss) and [charmbracelet/colorprofile](https://github.com/charmbracelet/colorprofile) for terminal colors
 - [fsnotify/fsnotify](https://github.com/fsnotify/fsnotify) for watching file changes in realtime
 - [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) for the embedded, pure-Go SQLite driver
 - [pressly/goose](https://github.com/pressly/goose) for database migrations
 - [oklog/ulid](https://github.com/oklog/ulid) for sortable snapshot ids
 - [klauspost/compress](https://github.com/klauspost/compress) for compressing stored file contents
 - [gofrs/flock](https://github.com/gofrs/flock) for the write lock
 - [git-pkgs/gitignore](https://github.com/git-pkgs/gitignore) for its `.sporignore` file
