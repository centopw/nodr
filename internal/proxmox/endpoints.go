package proxmox

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

func (c *Client) GetVersion(ctx context.Context) (Version, error) {
	var v Version
	err := c.Get(ctx, "/version", &v)
	return v, err
}

func (c *Client) GetClusterStatus(ctx context.Context) ([]ClusterNode, error) {
	var nodes []ClusterNode
	err := c.Get(ctx, "/cluster/status", &nodes)
	return nodes, err
}

func (c *Client) GetClusterResources(ctx context.Context, kind string) ([]ClusterResource, error) {
	path := "/cluster/resources"
	if kind != "" {
		path = fmt.Sprintf("%s?type=%s", path, url.QueryEscape(kind))
	}
	var res []ClusterResource
	err := c.Get(ctx, path, &res)
	return res, err
}

func (c *Client) GetNodeQEMU(ctx context.Context, node string) ([]QEMUSummary, error) {
	var vms []QEMUSummary
	err := c.Get(ctx, fmt.Sprintf("/nodes/%s/qemu", url.PathEscape(node)), &vms)
	return vms, err
}

func (c *Client) GetQEMUConfig(ctx context.Context, node string, vmid int) (QEMUConfig, string, error) {
	var raw map[string]any
	path := fmt.Sprintf("/nodes/%s/qemu/%d/config", url.PathEscape(node), vmid)
	if err := c.Get(ctx, path, &raw); err != nil {
		return QEMUConfig{}, "", err
	}
	digest, _ := raw["digest"].(string)

	settings := make(map[string]string, len(raw))
	for k, v := range raw {
		if k == "digest" {
			continue
		}
		settings[k] = fmt.Sprintf("%v", v)
	}

	return QEMUConfig{
		Digest:      digest,
		RawSettings: settings,
	}, digest, nil
}

func (c *Client) GetCertFingerprint(ctx context.Context, node string) (string, error) {
	var certs []CertificateInfo
	path := fmt.Sprintf("/nodes/%s/certificates/info", url.PathEscape(node))
	if err := c.Get(ctx, path, &certs); err != nil {
		return "", err
	}
	for _, cert := range certs {
		if cert.Fingerprint != "" {
			return cert.Fingerprint, nil
		}
	}
	return "", fmt.Errorf("proxmox: no certificate fingerprint found for node %s", node)
}

func (c *Client) CreateUser(ctx context.Context, userid string, opts UserOptions) error {
	payload := map[string]any{
		"userid": userid,
	}
	if opts.Comment != "" {
		payload["comment"] = opts.Comment
	}
	if opts.Email != "" {
		payload["email"] = opts.Email
	}
	if opts.Enable != 0 {
		payload["enable"] = opts.Enable
	}
	if opts.Expire != 0 {
		payload["expire"] = opts.Expire
	}
	if opts.Password != "" {
		payload["password"] = opts.Password
	}
	return c.Post(ctx, "/access/users", payload, nil)
}

func (c *Client) CreateRole(ctx context.Context, roleid string, privileges []string) error {
	payload := map[string]any{
		"roleid": roleid,
		"privs":  strings.Join(privileges, ","),
	}
	return c.Post(ctx, "/access/roles", payload, nil)
}

func (c *Client) UpdateACL(ctx context.Context, path string, roles map[string][]string) error {
	for subject, roleList := range roles {
		payload := map[string]any{
			"path":  path,
			"roles": strings.Join(roleList, ","),
		}
		if strings.Contains(subject, "@") {
			payload["users"] = subject
		} else {
			payload["groups"] = subject
		}
		if err := c.Put(ctx, "/access/acl", payload, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) CreateAPIToken(ctx context.Context, userid, tokenID string) (string, error) {
	path := fmt.Sprintf("/access/users/%s/token/%s", url.PathEscape(userid), url.PathEscape(tokenID))
	var res APITokenResult
	if err := c.Post(ctx, path, nil, &res); err != nil {
		return "", err
	}
	return res.Value, nil
}
