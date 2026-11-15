# Architecture of github-metrics

This directory follows the arc42 template. Each numbered page is one arc42 section. The diagrams come from one Structurizr file named `workspace.dsl`.

The service is under construction. Each page says what exists on main and what the design still plans. Commit `8e06fb1` of the `tbhb-dev/wiki` repository has the original design, in the `drafts/github-metrics` folder. These pages replace it.

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
| [appendix-a-trace-and-metric-catalog.md](appendix-a-trace-and-metric-catalog.md) | Appendix A: trace and metric catalog |
| [appendix-b-app-permissions-and-events.md](appendix-b-app-permissions-and-events.md) | Appendix B: permissions, manifest keys, and events |
| [appendix-c-github-limit-evidence.md](appendix-c-github-limit-evidence.md) | Appendix C: GitHub limit evidence |
| [appendix-d-alternatives-considered.md](appendix-d-alternatives-considered.md) | Appendix D: alternatives considered and rejected |
| [appendix-e-budget-dedupe-and-retention.md](appendix-e-budget-dedupe-and-retention.md) | Appendix E: budget, dedupe, and retention |

## Diagrams

`workspace.dsl` is the source. The files in `diagrams/` come from it with the Structurizr command-line tool and PlantUML, using these commands in this directory:

```sh
structurizr export -workspace workspace.dsl -format plantuml -output ./out
plantuml -tsvg ./out/structurizr-{SystemContext,Containers,Components}.puml
plantuml -tpng ./out/structurizr-{SystemContext,Containers,Components}.puml
structurizr export -workspace workspace.dsl -format json -output ./out
mv ./out/*.svg ./out/*.png ./out/workspace.json diagrams/
```

The pages link the SVG files. Each Portable Network Graphics (PNG) file shows the same view as the SVG file of the same name, for readers and tools that can't display SVG. `workspace.json` is the same workspace in the Structurizr JSON format, for tools that read JSON instead of the `workspace.dsl` syntax.

- `diagrams/structurizr-SystemContext.svg`
- `diagrams/structurizr-Containers.svg`
- `diagrams/structurizr-Components.svg`
- `diagrams/structurizr-SystemContext.png`
- `diagrams/structurizr-Containers.png`
- `diagrams/structurizr-Components.png`
- `diagrams/workspace.json`

No Bazel target lints the workspace file or checks these files for staleness yet, and the change that added the PNG and JSON files left that out on purpose. That work belongs to the shared rules targets requested from tbhb-dev/rules. The request covers `structurizr validate` as a test and a generate step with a staleness test. CI runs none of these exports until those targets exist. Until then, rerun the commands after each edit to `workspace.dsl` and commit the results.
