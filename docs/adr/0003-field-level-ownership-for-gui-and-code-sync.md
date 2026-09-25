# ADR-0003: Field-level ownership for GUI and code synchronization

- Status: Proposed
- Date: 2026-09-25
- Related: [§4 Dual-mode management and sync](../design/04-dual-mode-and-sync.md)

## Context

Beginners edit resources through forms while power users edit the generated
code of the same resources. Neither group may lose work. Forms cannot edit
expressions such as `var.web_cores`, and code can contain attributes that the
schema does not model.

## Decision

Every field of a managed resource is in exactly one ownership state:
*synced*, *code-owned*, *extension* or *ignored*. The state is derived from
the shape of the code on every sync (literal, expression or unmodeled
attribute) and from explicit pins (`# nodr:keep`) and ignore rules. The GUI
never overwrites a code-owned value without a confirmation that shows the code
diff. Regeneration is a three-way merge between the previous pristine output,
the new pristine output and the working code. Conflicts are shown field by
field.

## Consequences

Positive:

- Nothing is overwritten silently, in either direction.
- Power users keep full control of the code, and beginners keep a working GUI.
- The model is explainable: every field shows who owns it and why.

Negative, with mitigations:

- The concept is new to users. Badges, explanations and the "Take over" flow
  make it visible where it matters.
- Pristine refs and structural merge drivers are needed. They ship with nodr
  and the CLI.
- Some GUI fields become read-only when code uses expressions. This is
  intended: the alternative would be approximating code in a form.

## Alternatives considered

- **Last writer wins.** Silent data loss.
- **One-way generation with "do not edit" headers.** Power users lose their
  edits and eventually eject.
- **Code as the only truth.** Forms cannot represent code faithfully.
- **Locking whole resources to one mode.** Too coarse: one expression would
  take a whole VM out of the GUI.
