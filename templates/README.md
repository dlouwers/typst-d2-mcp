# House document templates

Typst packages, baked into the image, that documents import by name:

```typst
#import "@house/templates:0.1.0": report, adr
```

## Why the layout looks like this

typst resolves local packages from
`$XDG_DATA_HOME/typst/packages/<namespace>/<name>/<version>`, so this
directory tree *is* the import path. `XDG_DATA_HOME` is set to
`/usr/local/share` in the image, which is why templates ship read-only
alongside the binary while typst's writable `@preview` cache stays under
`$HOME` on the data volume.

Importing by package name rather than by file path is not a stylistic
choice: the compile root is the staged `.typ` file's temporary
directory, not the caller's workspace, so a relative import of a
workspace file would not resolve at all.

## Why `house` is a namespace and not a folder name

`house` is an **owner slug**. Today there is one owner and its packages
are baked into the image. The intended path is:

1. one owner, in the image — where this is now
2. owners' packages on the data volume, so house style changes without
   an image rebuild
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

Export a function from `lib.typ`. Two shapes exist deliberately:

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
