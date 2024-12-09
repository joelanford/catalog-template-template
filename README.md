# catalog-template-template

Generate FBC template files declaratively.

Using the tried and true OLM package manifests format with a few small additions,
we can generate FBC template files automatically based purely on the contents of
your package manifest directory. The extra things we need are:
- A mapping file in each bundle directory (`release-config.yaml`) that declares the catalog versions in which a bundle should be included
- A single template file in the package directory (`fbc-template.yaml.tmpl`) that provides your instructions for rendering the FBC templates

The data passed into the template includes:
- The catalog version targeted by this FBC template (`.CatalogVersion.{String,Major,Minor}`)
- The bundles included for this catalog version (`.Bundles[].{Name,Version,Image}`)
- Any values included in `fbc-template.values.yaml` (`.Values`)

## Prerequisites

- [`kpm`](https://github.com/joelanford/kpm) (somewhere in your $PATH)

`kpm` is used to do two things:
1. Build a bundle into a local OCI Archive-based kpm file, in order to generate a valid digest-based image reference
2. Render the temporary kpm file, in order to generate the `olm.bundle` blob from which we can extract the bundle's name, image, and version

NOTE: The digest-based references produced by `kpm` and provided to the templating engine are only valid if `kpm` also used to push the generated kpm files.

## Installation

```console
go install github.com/joelanford/catalog-template-template@latest
```

## Example

```console
pushd testdata
go run . ./testdata/cockroachdb
cat ./testdata/cockroachdb/catalog-templates/*
```