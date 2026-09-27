package cli

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
	"golang.org/x/term"

	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/planapply"
	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/proxmoxbootstrap"
	"github.com/centopw/nodr/internal/proxmoxdiscovery"
	"github.com/centopw/nodr/internal/secrets"
)

func (a *app) clusterCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage Proxmox cluster connections",
	}
	cmd.AddCommand(a.clusterConnectCommand())
	cmd.AddCommand(a.clusterDiscoverCommand())
	return cmd
}

func (a *app) clusterConnectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Connect a Proxmox VE cluster: bootstrap a least-privilege user and pin its TLS certificate",
		Long: `Connect walks through onboarding a Proxmox VE cluster: it asks for the
cluster's name, an endpoint URL, a node to fetch the TLS certificate
fingerprint from, and an administrator credential. After the operator
confirms the printed fingerprint, it creates a dedicated nodr@pve user,
NodrOperator role and API token on the cluster (docs/design/06-proxmox.md
§6.2), discards the administrator credential, stores the new token via the
workspace's secrets store, and writes a ProxmoxCluster intent document.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.clusterConnect(cmd.Context())
		},
	}
	return cmd
}

func (a *app) clusterConnect(ctx context.Context) error {
	loaded, err := a.mustLoad()
	if err != nil {
		return err
	}
	reader := bufio.NewReader(a.stdin)
	clusterName, err := readLine(reader, "Cluster name: ", a.stdout)
	if err != nil {
		return err
	}
	endpoint, err := readLine(reader, "Endpoint URL (e.g. https://10.0.0.1:8006): ", a.stdout)
	if err != nil {
		return err
	}
	node, err := readLine(reader, "Node to fetch the certificate from: ", a.stdout)
	if err != nil {
		return err
	}
	adminUser, err := readLine(reader, "Administrator username: ", a.stdout)
	if err != nil {
		return err
	}

	var adminPassword string
	if f, ok := a.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(a.stdout, "Administrator password: ")
		passwordBytes, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(a.stdout)
		if err != nil {
			return err
		}
		adminPassword = string(passwordBytes)
	} else {
		// Non-interactive fallback: read from reader
		adminPassword, err = readLine(reader, "Administrator password: ", a.stdout)
		if err != nil {
			return err
		}
	}

	insecureClient := proxmox.NewClient(endpoint, nil)
	if err := insecureClient.Login(ctx, "pam", adminUser, adminPassword); err != nil {
		return fmt.Errorf("nodr: cannot log in to %s: %w", endpoint, err)
	}
	fingerprint, err := insecureClient.GetCertFingerprint(ctx, node)
	if err != nil {
		return fmt.Errorf("nodr: cannot fetch certificate fingerprint: %w", err)
	}
	fmt.Fprintf(a.stdout, "Certificate fingerprint: %s\n", fingerprint)

	if a.interactive {
		approved, err := a.confirm(ctx)
		if err != nil {
			return err
		}
		if !approved {
			fmt.Fprintln(a.stderr, "nodr: cluster connect canceled")
			return errReported
		}
	} else {
		confirmLine, err := readLine(reader, "Accept fingerprint? (yes/no): ", a.stdout)
		if err != nil {
			return err
		}
		if confirmLine != "yes" && confirmLine != "y" {
			fmt.Fprintln(a.stderr, "nodr: cluster connect canceled")
			return errReported
		}
	}

	pinnedClient := proxmox.NewClient(endpoint, proxmox.NewPinnedHTTPClient(fingerprint))
	if err := pinnedClient.Login(ctx, "pam", adminUser, adminPassword); err != nil {
		return fmt.Errorf("nodr: cannot re-authenticate over the pinned connection: %w", err)
	}
	_ = adminPassword

	result, err := proxmoxbootstrap.Bootstrap(ctx, pinnedClient, proxmoxbootstrap.Privileges)
	if err != nil {
		return err
	}

	ref := "proxmox/" + clusterName + "-token"
	kek, err := secrets.LoadKEK(secrets.KEKConfig{EnvVar: "NODR_KEK"})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(loaded.ws.Root, ".nodr"), 0700); err != nil {
		return err
	}
	store, err := secrets.Open(filepath.Join(loaded.ws.Root, ".nodr", "secrets.db"), kek)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Put(ctx, ref, []byte(result.TokenSecret)); err != nil {
		return err
	}

	doc := struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec v1alpha1.ProxmoxClusterSpec `yaml:"spec"`
	}{
		APIVersion: "nodr/v1alpha1",
		Kind:       v1alpha1.KindProxmoxCluster,
	}
	doc.Metadata.Name = clusterName
	doc.Spec = v1alpha1.ProxmoxClusterSpec{
		Endpoints:      []string{endpoint},
		CredentialsRef: ref,
		Nodes:          []string{node},
		TLS:            &v1alpha1.ProxmoxTLS{Fingerprint: fingerprint},
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	intentPath := filepath.Join(loaded.ws.Root, "intent", "platform", clusterName+".yaml")
	if err := os.MkdirAll(filepath.Dir(intentPath), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(intentPath, out, 0644); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "connected cluster %q; wrote %s\n", clusterName, intentPath)
	return nil
}

func (a *app) clusterDiscoverCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "discover <cluster>",
		Short: "List live QEMU guests on a connected Proxmox VE cluster and classify them against workspace intent",
		Long: `Discover connects to a cluster nodr already manages (docs/design/06-proxmox.md
§6.3) using its stored API token, lists every live QEMU guest, and reports
whether nodr already manages it, it looks undiscovered, or its tags or
description mention another infrastructure-as-code tool. Discover makes no
changes: it neither writes intent nor touches the cluster.`,
		Args: exactlyOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.clusterDiscover(cmd.Context(), args[0])
		},
	}
	return cmd
}

func (a *app) clusterDiscover(ctx context.Context, clusterName string) error {
	loaded, err := a.mustLoad()
	if err != nil {
		return err
	}

	credentialsRef, ok := planapply.CredentialsRefFor(loaded.ws, clusterName)
	if !ok {
		return fmt.Errorf("nodr: cluster %q not found, or has no credentialsRef; run 'nodr cluster connect' first", clusterName)
	}
	resolve, err := a.secretsResolve(loaded.ws.Root)
	if err != nil {
		return err
	}
	if resolve == nil {
		return fmt.Errorf("nodr: no secrets store found at %s; run 'nodr cluster connect' first", filepath.Join(loaded.ws.Root, ".nodr", "secrets.db"))
	}
	secret, err := resolve(ctx, credentialsRef)
	if err != nil {
		return fmt.Errorf("nodr: resolve %s: %w", credentialsRef, err)
	}

	d := loaded.ws.Find(nrm.Ref{Kind: v1alpha1.KindProxmoxCluster, Name: clusterName})
	if d == nil {
		return fmt.Errorf("nodr: cluster %q not found", clusterName)
	}
	spec, err := v1alpha1.Decode[v1alpha1.ProxmoxClusterSpec](d)
	if err != nil {
		return fmt.Errorf("nodr: decode cluster %q: %w", clusterName, err)
	}
	if len(spec.Spec.Endpoints) == 0 {
		return fmt.Errorf("nodr: cluster %q has no endpoints", clusterName)
	}
	var httpClient *http.Client
	if spec.Spec.TLS != nil && spec.Spec.TLS.Fingerprint != "" {
		httpClient = proxmox.NewPinnedHTTPClient(spec.Spec.TLS.Fingerprint)
	}
	client := proxmox.NewClient(spec.Spec.Endpoints[0], httpClient)
	client.SetAPIToken(proxmoxbootstrap.BootstrapUser, proxmoxbootstrap.BootstrapTokenID, string(secret))

	guests, err := proxmoxdiscovery.Discover(ctx, client, loaded.ws, clusterName)
	if err != nil {
		return err
	}

	rows := [][]string{{"VMID", "NAME", "NODE", "STATUS", "CPU", "MEMORY", "NODR"}}
	for _, g := range guests {
		rows = append(rows, []string{
			strconv.Itoa(g.VMID), g.Name, g.Node, g.Status,
			strconv.Itoa(g.CPUs), g.Memory, string(g.Classified),
		})
	}
	return writeTable(a.stdout, 2, rows)
}
