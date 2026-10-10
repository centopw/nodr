package ledger

import (
	"errors"
	"testing"
)

func TestConnect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		files   map[string]string
		input   ConnectInput
		wantErr error
	}{
		{
			name:  "valid workspace connects under manifest name",
			files: workspaceFiles,
			input: ConnectInput{InstallationID: testInstall, Owner: testOwner, Repository: testRepo},
		},
		{
			name:    "repository outside the installation is inaccessible",
			files:   workspaceFiles,
			input:   ConnectInput{InstallationID: testInstall, Owner: testOwner, Repository: "other"},
			wantErr: ErrRepositoryInaccessible,
		},
		{
			name:    "unknown installation lists no repositories",
			files:   workspaceFiles,
			input:   ConnectInput{InstallationID: 999, Owner: testOwner, Repository: testRepo},
			wantErr: ErrRepositoryInaccessible,
		},
		{
			name:    "repository without a manifest is not a nodr repository",
			files:   map[string]string{"README.md": "hello\n"},
			input:   ConnectInput{InstallationID: testInstall, Owner: testOwner, Repository: testRepo},
			wantErr: ErrNotNodrRepository,
		},
		{
			name:    "manifest of another kind is not a nodr repository",
			files:   map[string]string{"nodr.yaml": "apiVersion: nodr/v1alpha1\nkind: Network\nmetadata: { name: dmz }\nspec: {}\n"},
			input:   ConnectInput{InstallationID: testInstall, Owner: testOwner, Repository: testRepo},
			wantErr: ErrNotNodrRepository,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, tt.files)
			ws, err := env.svc.Connect(t.Context(), tt.input)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Connect error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if ws.Slug != testSlug || ws.Owner != testOwner || ws.DefaultSHA == "" {
				t.Fatalf("Connect = %+v, want slug %q owned by %s with a SHA", ws, testSlug, testOwner)
			}
		})
	}
}

func TestConnectRejectsDuplicateWorkspace(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	env.connect(t)
	_, err := env.svc.Connect(t.Context(), ConnectInput{InstallationID: testInstall, Owner: testOwner, Repository: testRepo})
	if !errors.Is(err, ErrWorkspaceExists) {
		t.Fatalf("second Connect error = %v, want ErrWorkspaceExists", err)
	}
}

func TestInstallationsListRepositories(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	installs, err := env.svc.Installations(t.Context())
	if err != nil {
		t.Fatalf("Installations: %v", err)
	}
	if len(installs) != 1 || installs[0].ID != testInstall {
		t.Fatalf("Installations = %+v, want one installation %d", installs, testInstall)
	}
	repos := installs[0].Repositories
	if len(repos) != 1 || repos[0].Owner != testOwner || repos[0].Name != testRepo || repos[0].DefaultBranch != "main" {
		t.Fatalf("repositories = %+v", repos)
	}
}

func TestWorkspaceAfterConnectReportsDefaultSHA(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	connected := env.connect(t)
	got, err := env.svc.Workspace(t.Context(), testSlug)
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if got.DefaultSHA != connected.DefaultSHA || got.DefaultSHA != originRef(t, env.origin, "refs/heads/main") {
		t.Fatalf("Workspace SHA = %s, want %s", got.DefaultSHA, connected.DefaultSHA)
	}
	if _, err := env.svc.Workspace(t.Context(), "missing"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("Workspace(missing) error = %v, want ErrWorkspaceNotFound", err)
	}
}
