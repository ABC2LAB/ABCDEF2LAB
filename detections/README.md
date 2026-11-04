# Inherited detection references

These rules, indicators, and tests are retained from the base project. See the [ABC2LAB documentation](../docs/README.md) for the current product and implementation status.
A rule match or a passing suite here is not evidence that ABC2LAB's request controls, evidence decisions, or complete detection workflow have been validated.

English · [한국어](README.ko.md)

## Contents

- [Sigma rules](sigma/README.md)
- [Suricata rules](suricata/README.md)
- [ATT&CK layer](attack/README.md)
- [Indicators and MISP data](indicators/README.md)
- [Read-only host triage](triage/README.md)
- [Tests and dependencies](tests/README.md)
- [Background guide (Korean)](../docs/defense-ko.md)

## Current prerequisites

The inherited runner and some indicator/MISP checks read `.github/workflows/detections.yml`, which is absent from this checkout.
Do not treat the full runner as passing or automatically enforced by CI. Check the actual dependencies before running it; adapting the harness and registering CI are separate changes.

Keep source indicators, rules, CSV/MISP exports, ATT&CK tags, and reproducible tests aligned when changing these references.
Run only in an owned or approved isolated environment and record the source SHA, commands, results, failures, and limits.
Follow the [ABC2LAB contribution guide (Korean)](../CONTRIBUTING.md).

## Sigma backend portability

See the [Sigma reference](sigma/README.md) and [test documentation](tests/README.md) for backend commands and limitations. Their inherited sample outputs are not results from this checkout.
