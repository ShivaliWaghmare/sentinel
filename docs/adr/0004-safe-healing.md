# ADR-0004: Healing changes locators only, is verified, and needs human approval
Status: Accepted

## Context
An auto-healer that edits assertions can make a real bug look like a passing test. That is the worst possible failure for a test system.

## Decision
The Healer only handles locator failures. A patch guard rejects changes to expected values, test titles or the number of expect calls. Patches are verified by two passing runs on a temporary copy, and applied to the real file only after a human approves.

## Consequences
- Fewer automatic fixes, but no silent bug masking.
- "Unsafe heal rate" becomes an eval metric with a target of 0.
