package repository

import (
	"context"
	"fmt"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/merkletrie"
)

// FileDiff describes one file changed between two commits.
type FileDiff struct {
	Path   string // forward slashes
	Status string // "added" | "modified" | "deleted"
	Patch  string // unified diff; empty for binary
}

// Diff returns the file-level changes from baseSHA to headSHA in the workspace mirror.
func (s *LocalStore) Diff(ctx context.Context, slug, baseSHA, headSHA string) ([]FileDiff, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, err := git.PlainOpen(s.mirrorPath(slug))
	if err != nil {
		return nil, fmt.Errorf("repository: open workspace: %w", err)
	}
	baseTree, err := commitTree(repo, baseSHA)
	if err != nil {
		return nil, fmt.Errorf("repository: resolve diff base: %w", err)
	}
	headTree, err := commitTree(repo, headSHA)
	if err != nil {
		return nil, fmt.Errorf("repository: resolve diff head: %w", err)
	}
	changes, err := baseTree.DiffContext(ctx, headTree)
	if err != nil {
		return nil, fmt.Errorf("repository: diff trees: %w", err)
	}
	diffs := make([]FileDiff, 0, len(changes))
	for _, change := range changes {
		action, err := change.Action()
		if err != nil {
			return nil, fmt.Errorf("repository: classify change: %w", err)
		}
		entry, err := fileDiffOf(ctx, change, action)
		if err != nil {
			return nil, err
		}
		diffs = append(diffs, entry)
	}
	return diffs, nil
}

// fileDiffOf converts one tree change into a FileDiff with its unified patch.
func fileDiffOf(ctx context.Context, change *object.Change, action merkletrie.Action) (FileDiff, error) {
	var status, path string
	switch action {
	case merkletrie.Insert:
		status, path = "added", change.To.Name
	case merkletrie.Delete:
		status, path = "deleted", change.From.Name
	case merkletrie.Modify:
		status, path = "modified", change.To.Name
	default:
		return FileDiff{}, fmt.Errorf("repository: unknown change action %v", action)
	}
	patch, err := object.Changes{change}.PatchContext(ctx)
	if err != nil {
		return FileDiff{}, fmt.Errorf("repository: patch %s: %w", path, err)
	}
	for _, filePatch := range patch.FilePatches() {
		if filePatch.IsBinary() {
			return FileDiff{Path: path, Status: status}, nil
		}
	}
	return FileDiff{Path: path, Status: status, Patch: patch.String()}, nil
}

// commitTree resolves a commit SHA in repo to its root tree.
func commitTree(repo *git.Repository, sha string) (*object.Tree, error) {
	commit, err := repo.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return nil, err
	}
	return commit.Tree()
}
