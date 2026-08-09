# Administration API contract

`openapi.yaml` is the source contract for the breaking `/api/v2` browser API.
The server deliberately does not register `/api/v1` compatibility routes.

Validate the specification with a pinned tool version:

```sh
npx --yes @redocly/cli@2.46.0 lint api/openapi.yaml --config api/redocly.yaml
```

Generate deterministic TypeScript transport types when wiring the UI client:

```sh
npx --yes openapi-typescript@7.13.0 api/openapi.yaml --output web/ui/src/shared/api/generated/schema.d.ts
```

Generated files must not be edited. Runtime parsing remains a boundary concern;
it must validate the same shapes instead of redefining a second transport model.
