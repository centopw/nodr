# ADR-0004: OpenTofu as the default Terraform-compatible engine

- Status: Proposed
- Date: 2026-09-25
- Related: [§5.5 Engine code conventions](../design/05-compiler-and-engines.md#55-engine-code-conventions),
  [§10.5 Secrets management](../design/10-security.md#105-secrets-management)

## Context

nodr generates HCL that users expect to edit as "Terraform files", and it has
to ship an engine that runs that code in its images and appliances. Terraform
has been released under the Business Source License since August 2023. Its
restrictions on competing offerings create uncertainty for a product that
bundles and orchestrates the engine. OpenTofu is an open-source fork under the
MPL 2.0, governed under the Linux Foundation umbrella. It keeps language
compatibility and adds state and plan encryption, and, since 1.11, ephemeral
values and write-only attributes.

## Decision

nodr bundles OpenTofu as the default executor. Rendered HCL stays within the
language subset shared by OpenTofu 1.8+ and Terraform 1.8+. Workspaces that
require Terraform can use a Terraform CLI binary supplied by the user.
Engine-specific features, such as OpenTofu state encryption through
`TF_ENCRYPTION`, are configured at runtime outside the code, so the code stays
portable.

## Consequences

Positive:

- The engine can be redistributed with nodr without license uncertainty.
- State and plan encryption and ephemeral secrets are available by default.
- Generated code still works with Terraform, which keeps the export guarantee
  for Terraform users.

Negative, with mitigations:

- Compatibility with two engines must be tracked. CI plans sample workspaces
  with both.
- OpenTofu-only language features are not used in shared code. Whether
  workspaces pinned to OpenTofu may use them is an open question
  ([§13.10](../design/13-operations-and-delivery.md#1310-open-questions)).

## Alternatives considered

- **Bundling Terraform.** License uncertainty for redistribution.
- **Supporting only a Terraform binary supplied by the user.** A worse
  first-run experience and no state encryption by default.
- **Pulumi as the default.** A different programming model that is less
  familiar to the target users. It could still be added as a plugin.
