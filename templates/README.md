# House document templates

Typst packages that documents import by name. They are embedded in the
binary and seeded onto the data volume on startup, so an operator can
change them there without an image rebuild:

```typst
#import "@house/templates:0.1.0": report, adr
```

## Why the layout looks like this

typst resolves local packages from
`$XDG_DATA_HOME/typst/packages/<namespace>/<name>/<version>`, so this
directory tree *is* the import path.

In the image `XDG_DATA_HOME` is `/var/lib/typst-d2-mcp/data`, which sits
on the data volume. This tree is embedded in the binary and **seeded**
there on startup rather than shipped read-only: `seedBundledTemplates`
copies any file that is not already present and never overwrites one
that is. That is step 2 of #63 — house style changes on the volume
without an image rebuild — and it is what makes the operator path below
possible. The seed target is a sibling of the workspace tree
(`/var/lib/typst-d2-mcp/workspaces`), not inside it, so the sweeper
never sees it. typst's writable `@preview` cache stays under `$HOME`.

Importing by package name rather than by file path is not a stylistic
choice: the compile root is the staged `.typ` file's temporary
directory, not the caller's workspace, so a relative import of a
workspace file would not resolve at all.

## Why `house` is a namespace and not a folder name

`house` is an **owner slug**. Today there is one owner. The intended
path is:

1. one owner, in the image — done
2. owners' packages on the data volume, so house style changes without
   an image rebuild — **where this is now** (see the seeding note above)
3. owners become organisations in the schema, with users able to belong
   to more than one
4. each compile resolves only the namespaces its caller can see — the
   same sandboxing discipline as `workspace.ScopedFS`
5. organisation-scoped template management through the admin UI

Storage moves at every step. The import path in people's documents does
not, which is the point of putting the owner in the namespace: a user in
three organisations has no "current organisation" to select, because the
document already says which one it means.

## Adding a document type

There are two paths, and they differ in who can take them and how long
the result lasts.

### In the repo (permanent)

Export a function from `lib.typ`. This is the path for a type that
should exist for everyone, on every deployment: it ships in the binary,
is covered by the tests below, and seeds onto every volume.

### On the data volume (operator, immediate)

Because seeding never overwrites, anything already at
`$XDG_DATA_HOME/typst/packages/` wins. An operator can edit
`house/templates/0.1.0/lib.typ` in place, or drop in an entirely new
namespace or version, and it survives restarts and image upgrades. This
is the intended way to change house style without a redeploy.

Four things to know before using it:

- **Nothing validates the result.** A syntax error in `lib.typ` breaks
  every compile that imports it, and the first sign is a failing user
  compile. Check the edit by compiling a document that imports it
  before walking away.
- **Files must be readable by uid 65532** (`nonroot`), which the server
  runs as.
- **A new package is not advertised.** `templateInstructions()` names
  exactly `@house/templates:0.1.0` (see the `templateNamespace` /
  `templateName` / `templateVersion` constants), so a type added under a
  different namespace or version compiles fine but no caller is told it
  exists. Callers have to be told out of band until a `list_templates`
  tool lands (#90).
- **Edits are invisible to the repo.** Anything worth keeping should
  come back here as a change to `lib.typ`, or the next volume will not
  have it.

### Either way

Two shapes exist deliberately:

- `report` owns the **look**. The author writes what they like.
- `adr` owns the **structure** too — its sections are arguments, not
  headings the author has to remember, so every ADR carries the same
  ones in the same order.

Reach for the second shape when a document type has a required shape;
consistent styling over inconsistent structure is only half the job.

Two Typst constraints worth knowing before adding one:

- `context` is a reserved word and cannot be a parameter name. `adr`
  takes `background:` and renders the heading as "Context".
- A trailing body must be **positional**. `#show: adr.with(...)` passes
  the rest of the document positionally, and a named `body:` parameter
  rejects it.

Both are covered by tests in `cmd/typst-d2-mcp/templates_test.go`, which
compile every type through the real typst binary.
