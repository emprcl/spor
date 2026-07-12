# spor

:notebook: **[Specification](docs/SPEC.md)**

spor is a versioning tool for creative workflows. It works differently from
traditional version control like [Git](https://git-scm.com/): instead of manual
commits, spor watches your project and automatically saves a snapshot every time
a file changes. You can then jump back to any past state, or branch off from one
to explore a different path. Think of it as infinite undo for your whole project.

Everything is automatic and local. spor records your history as an immutable
graph of states as you work, storing each unique file once (deduplicated and
compressed), so returning to any moment is instant and nothing is ever lost.
There are no commits to write, nothing to stage, and no branches to manage.

**_spor is a work-in-progress. The command surface and on-disk format should not
be considered stable until 1.0._**

_Feel free to [open an issue](https://github.com/emprcl/spor/issues/new)._

## Usage

Early development: the `snapshot` command works today. Run it inside a project to
record its current state.

```sh
spor snapshot            # record the current project state
spor snapshot -m "wip"   # ...with a label
```

## Documentation

See the [Specification](docs/SPEC.md) for the full design and the mental model
behind spor.
