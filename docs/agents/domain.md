# Domain Docs

Use this workspace's domain documentation when exploring or changing code.

## Before exploring

- Read `GLOSSARY.md` at the root if it exists.
- Read ADRs in `docs/adr/` that relate to the area being changed.
- If `GLOSSARY-MAP.md` exists, read it and the glossary for the relevant context. For context-scoped code, also check `src/<context>/docs/adr/`.

If these files do not exist, proceed without creating them upfront. The domain-modeling skill creates them when terms or decisions are resolved.

## Use the glossary

Use domain terms as defined in the relevant glossary. If a needed concept is missing, note the gap for domain modeling.

## Flag ADR conflicts

If a change contradicts an existing ADR, name the ADR and explain why it may need to be revisited.

## Layout

This workspace uses a single-context layout: a root `GLOSSARY.md` and `docs/adr/` for system decisions.
