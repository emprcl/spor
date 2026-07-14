# spor

:notebook: **[Design Specification](docs/design-spec.md)**

spor is a versioning tool for creative workflows, built for the moments when you
don't know where you're headed yet, only that you don't want to lose where
you've been.

It works differently from traditional version control like
[Git](https://git-scm.com/). There are no commits to write, nothing to stage, no
branches to manage. Instead, spor watches your project and automatically saves a
snapshot every time something changes. You can jump back to any past moment, pick
up from there, and go a different way: think of it as infinite undo for your
whole project.

Everything is automatic and local. spor records your history as an immutable
graph of states as you work, storing each unique file once (deduplicated and
compressed), so returning to any moment is instant and nothing is ever lost.

It's especially suited to exploratory work, when you're trying things out,
backtracking, and changing direction without a plan mapped out in advance.

spor was built with creative coding workflows in mind first, but we think the
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

**_spor is a work-in-progress. The command surface and on-disk format should not
be considered stable until 1.0._**

_Feel free to [open an issue](https://github.com/emprcl/spor/issues/new)._

## Installation

spor is early and has no packaged releases yet. Build it from source with a
recent Go toolchain (1.25 or newer):

```sh
go build -o spor ./cmd/spor
```

## Usage

Run any command inside your project directory. There is nothing to set up: the
first snapshot creates spor's store automatically.

```sh
# Record snapshots
spor snap                # save the current state now
spor watch               # save automatically as you work (Ctrl+C to stop)

# Look around
spor log                 # show the history, newest first
spor status              # where you are and how large the history is
spor diff "2h ago"       # what changed since then

# Move through history
spor go 2h ago           # jump back to how things were
spor go @~2              # or go back two states
spor go 01ARZ7           # or jump to a specific state by id
spor undo                # step back one state (redo steps forward)
spor label @ v1          # name a state to return to later
```

Run `spor --help`, or `spor <command> --help`, for the full command list with
examples.

## Documentation

See the [Design Specification](docs/design-spec.md) for the full design and the
mental model behind spor.
