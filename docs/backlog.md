# Backlog

## Document that a step's retry policy governs its compensation

ADR-0021 keeps one retry policy per step for both the action and the compensation. The saga literature assumes "persistence of compensation" (Zhang, Nodine and Bhargava, TKDE 2001). The user ruled in the S5 piece 5 review (G2): "Manter ADR-0021 + doc (Recommended)". The godoc and the docs do not yet say that `NoRetry` on a step also makes its compensation a single attempt. The S7 docs sub-spec does not cover it.

Checklist when picked up: in the S7 docs sub-spec, write it into the godoc of `Step.Retry` and `NoRetry` and into the how-to on compensation failures. Say how to choose a persistent compensation (more attempts, bounded by `RecoveryTimeout`), and how Block and Halt serve as manual intervention.

*(staged 2026-11-21, priority: medium)*
