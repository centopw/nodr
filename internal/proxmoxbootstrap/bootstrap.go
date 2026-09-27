// Package proxmoxbootstrap sequences the Proxmox VE API calls that create a
// dedicated, least-privilege nodr user, role and API token (design
// docs/design/06-proxmox.md §6.2), reusing internal/proxmox's client.
package proxmoxbootstrap

import (
	"context"
	"fmt"

	"github.com/centopw/nodr/internal/proxmox"
)

// Result holds everything the caller needs to store and record. TokenSecret
// is the plaintext API token secret; the caller must store it via
// secrets.Store.Put and must not log it.
type Result struct {
	User        string
	Role        string
	TokenID     string
	TokenSecret string
}

const (
	bootstrapUser    = "nodr@pve"
	bootstrapRole    = "NodrOperator"
	bootstrapTokenID = "nodr"
)

// Bootstrap creates the dedicated user, role and API token on the cluster
// client authenticates against, and grants the role on "/" for the new
// user. client must already be authenticated as an administrator (via
// Login or SetAPIToken); Bootstrap does not manage that credential's
// lifetime — the caller discards it after this call returns.
func Bootstrap(ctx context.Context, client *proxmox.Client, privileges []string) (Result, error) {
	if err := client.CreateUser(ctx, bootstrapUser, proxmox.UserOptions{Comment: "created by nodr cluster connect"}); err != nil {
		return Result{}, fmt.Errorf("proxmoxbootstrap: create user: %w", err)
	}
	if err := client.CreateRole(ctx, bootstrapRole, privileges); err != nil {
		return Result{}, fmt.Errorf("proxmoxbootstrap: create role: %w", err)
	}
	if err := client.UpdateACL(ctx, "/", map[string][]string{bootstrapUser: {bootstrapRole}}); err != nil {
		return Result{}, fmt.Errorf("proxmoxbootstrap: update ACL: %w", err)
	}
	secret, err := client.CreateAPIToken(ctx, bootstrapUser, bootstrapTokenID)
	if err != nil {
		return Result{}, fmt.Errorf("proxmoxbootstrap: create API token: %w", err)
	}
	return Result{
		User:        bootstrapUser,
		Role:        bootstrapRole,
		TokenID:     bootstrapTokenID,
		TokenSecret: secret,
	}, nil
}

// Privileges is the fixed privilege list for NodrOperator (design
// docs/design/06-proxmox.md §6.2's table), covering Proxmox VE 8.x and 9.x.
var Privileges = []string{
	"VM.Allocate", "VM.Audit", "VM.Clone", "VM.Config.CDROM", "VM.Config.CPU",
	"VM.Config.Cloudinit", "VM.Config.Disk", "VM.Config.HWType", "VM.Config.Memory",
	"VM.Config.Network", "VM.Config.Options", "VM.PowerMgmt", "VM.Migrate",
	"VM.Snapshot", "VM.Snapshot.Rollback", "VM.Backup", "VM.Console",
	"Datastore.Allocate", "Datastore.AllocateSpace", "Datastore.AllocateTemplate", "Datastore.Audit",
	"Pool.Allocate", "Pool.Audit", "Mapping.Use", "Mapping.Audit",
	"SDN.Use", "SDN.Audit",
	"Sys.Audit", "Sys.Modify",
}
