package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
	"golang.org/x/term"

	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/proxmoxbootstrap"
	"github.com/centopw/nodr/internal/secrets"
)

func (a *app) clusterCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage Proxmox cluster connections",
	}
	cmd.AddCommand(a.clusterConnectCommand())
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
