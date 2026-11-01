# Issue tracker: Local Markdown

Issues and specs for this workspace live as Markdown files in `.scratch/`.

## Conventions

- One feature per directory: `.scratch/<feature-slug>/`
- The spec is `.scratch/<feature-slug>/spec.md`
- Implementation issues are one file per ticket at `.scratch/<feature-slug>/issues/<NN>-<slug>.md`, numbered from `01`, never one combined tickets file
- Triage state is recorded as a `Status:` line near the top of each issue file; see `triage-labels.md` for the role strings
- Comments and conversation history append to the bottom under `## Comments`

## Tracker operations

- To publish work, create a file under `.scratch/<feature-slug>/`, creating the directory if needed.
- To fetch a ticket, read the referenced file path or issue number.

## Wayfinding

- Map: `.scratch/<effort>/map.md`, containing Notes, Decisions-so-far, and Fog sections
- Child ticket: `.scratch/<effort>/issues/NN-<slug>.md`, with a `Type:` line (`research`, `prototype`, `grilling`, or `task`) and a `Status:` line (`claimed` or `resolved`)
- Blocking: `Blocked by: NN, NN`; a ticket is unblocked when every listed ticket is resolved
- Frontier: scan `issues/` for open, unblocked, unclaimed tickets; the lowest number goes first
- Claim: set `Status: claimed` before starting work
- Resolve: append the answer under `## Answer`, set `Status: resolved`, then add a context pointer to the map's Decisions-so-far
