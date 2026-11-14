# Architecture of github-metrics

This directory follows the arc42 template. Each numbered page is one arc42 section. The diagrams come from one Structurizr file named `workspace.dsl`.

The service is under construction. Each page says what exists on main and what the design still plans. These pages summarize the design in the tbhb-dev wiki, under `drafts/github-metrics`.

| Page | arc42 section |
| --- | --- |
| [01-introduction-and-goals.md](01-introduction-and-goals.md) | Introduction and goals |
| [02-constraints.md](02-constraints.md) | Constraints |
| [03-context-and-scope.md](03-context-and-scope.md) | Context and scope |
| [04-solution-strategy.md](04-solution-strategy.md) | Solution strategy |
| [05-building-block-view.md](05-building-block-view.md) | Building block view |
| [06-runtime-view.md](06-runtime-view.md) | Runtime view |
| [07-deployment-view.md](07-deployment-view.md) | Deployment view |
| [08-crosscutting-concepts.md](08-crosscutting-concepts.md) | Cross-cutting concepts |
| [09-architecture-decisions.md](09-architecture-decisions.md) | Architecture decisions |
| [10-quality-requirements.md](10-quality-requirements.md) | Quality requirements |
| [11-risks-and-technical-debt.md](11-risks-and-technical-debt.md) | Risks and technical debt |
| [12-glossary.md](12-glossary.md) | Glossary |

## Diagrams

`workspace.dsl` is the source. The operator plans a Bazel target to check it, then write `workspace.json` here and the images to `diagrams/`. That target hasn't merged, so none of these outputs exist yet. Once it does, nobody edits them by hand. The pages link these expected files:

- `diagrams/structurizr-SystemContext.svg`
- `diagrams/structurizr-Containers.svg`
- `diagrams/structurizr-Components.svg`
