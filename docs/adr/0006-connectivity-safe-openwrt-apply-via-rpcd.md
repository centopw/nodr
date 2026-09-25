# ADR-0006: Connectivity-safe OpenWrt changes through rpcd

- Status: Proposed
- Date: 2026-09-25
- Related: [§8.4 OpenWrt integration](../design/08-networking.md#84-openwrt-integration)

## Context

A router change can cut off the system that is applying it. OpenWrt's rpcd
offers an apply-and-confirm workflow for UCI (`uci apply` with rollback, then
`uci confirm`), which LuCI uses to avoid soft-bricking devices. Ansible usually
manages OpenWrt over SSH without that protection, and OpenWrt has no Python by
default. Terraform providers for OpenWrt cover only part of the configuration.

## Decision

nodr uses its own UCI executor, which talks to rpcd over ubus JSON-RPC on
HTTPS and applies every change with automatic rollback and explicit
confirmation after health probes. Over SSH, the executor emulates the same
protocol with a timed restore script. UCI files are the code representation.
Ansible remains available as an alternative engine.

## Consequences

Positive:

- A bad change heals itself, even when it cuts off nodr.
- rpcd ACLs give nodr least-privilege access.
- Routers need no Python.

Negative, with mitigations:

- nodr maintains a custom executor. It is small and tested against OpenWrt
  images in QEMU, including deliberate lock-out scenarios.
- It depends on `rpcd` and `uhttpd-mod-ubus`. LuCI installs them; for minimal
  images, nodr lists the packages to add or falls back to SSH.

## Alternatives considered

- **Ansible over SSH.** No built-in rollback.
- **A Terraform OpenWrt provider.** Limited coverage of UCI packages.
- **Replacing whole configuration files and rebooting.** Disruptive, and a
  mistake still locks the operator out.
