# D10. Repository and author type are the only metric attributes

- **Status:** accepted, 2026-11-14.
- **Rule:** the metric names, units, and bucket boundaries match the README of the original design. The PR number and the author's login never become metric attributes, and only milestones less than 24 hours old and later than the store's creation time count. The worker commits a milestone before it records the observation.
- **Undo:** rename or add instruments in a later release. Old series stop, and new ones start empty.
