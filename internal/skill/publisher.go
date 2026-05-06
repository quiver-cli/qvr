package skill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/registry"
)

var (
	ErrPublishNoRegistry = errors.New("no registry configured; pass --registry or set default_registry")
	ErrInvalidSkillPath  = errors.New("invalid local skill path")
)

// PublishRequest drives a publish operation.
type PublishRequest struct {
	LocalPath   string // path to a local skill directory containing SKILL.md
	Registry    string // registry name; falls back to config default
	Branch      string // target branch on the registry (defaults to registry default)
	Tag         string // optional annotated tag created at the new commit
	Message     string
	Author      string
	AuthorEmail string
	DryRun      bool
	// NoCreateBranch refuses to invent a new branch if Branch doesn't exist
	// on origin. Default behaviour is to branch from the registry default,
	// so "publish to a feature branch then open a PR" works without the
	// user having to pre-create the branch with raw git.
	NoCreateBranch bool
}

// PublishResult summarises a publish outcome.
type PublishResult struct {
	Skill    string `json:"skill"`
	Registry string `json:"registry"`
	Branch   string `json:"branch"`
	Tag      string `json:"tag,omitempty"`
	Commit   string `json:"commit"`
	DryRun   bool   `json:"dry_run"`
}

// Publisher copies a local skill into a registry repo and pushes upstream.
// It works through a transient full clone of the registry's bare repo rather
// than reusing an existing worktree so publishes don't collide with installs.
type Publisher struct {
	Git git.GitClient
}

// NewPublisher constructs a Publisher.
func NewPublisher(gc git.GitClient) *Publisher { return &Publisher{Git: gc} }

// Publish validates the local skill, clones the target registry into a temp
// dir, copies the skill into skills/<name>/, commits, and pushes.
func (p *Publisher) Publish(ctx context.Context, req PublishRequest) (*PublishResult, error) {
	abs, err := filepath.Abs(req.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	skill, err := LoadFromPath(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSkillPath, err)
	}
	if vr := Validate(skill); !vr.Valid {
		var msgs []string
		for _, e := range vr.Errors {
			msgs = append(msgs, e.Error())
		}
		return nil, fmt.Errorf("skill validation failed:\n  %s", strings.Join(msgs, "\n  "))
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	regName := req.Registry
	if regName == "" {
		regName = cfg.DefaultRegistry
	}
	if regName == "" {
		return nil, ErrPublishNoRegistry
	}
	regCfg, ok := cfg.Registries[regName]
	if !ok {
		return nil, fmt.Errorf("registry %q not configured", regName)
	}

	barePath := registry.RegistryPath(regName)
	defaultBranch, _ := p.Git.DefaultBranch(barePath)
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	branch := req.Branch
	if branch == "" {
		branch = defaultBranch
	}

	tmp, err := os.MkdirTemp("", "quiver-publish-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	stageDir := filepath.Join(tmp, "stage")
	if err := p.Git.Clone(ctx, regCfg.URL, stageDir); err != nil {
		return nil, fmt.Errorf("clone registry: %w", err)
	}

	repo, err := gogit.PlainOpen(stageDir)
	if err != nil {
		return nil, fmt.Errorf("open stage: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}

	// Ensure the stage is on the target branch. For the default branch this
	// is a no-op; for a non-default branch we check out the remote-tracking
	// ref as a new local branch. When the branch is new to origin, branch
	// it from the registry default so "publish to feature branch + open PR"
	// doesn't require users to drop into raw git (bug #14).
	autoCreateFrom := ""
	if !req.NoCreateBranch {
		autoCreateFrom = "origin/" + defaultBranch
	}
	if err := checkoutPublishBranch(repo, wt, branch, autoCreateFrom); err != nil {
		return nil, fmt.Errorf("checkout %s: %w", branch, err)
	}

	dest := filepath.Join(stageDir, "skills", skill.Frontmatter.Name)
	if err := copyDir(abs, dest); err != nil {
		return nil, fmt.Errorf("copy skill: %w", err)
	}
	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		return nil, fmt.Errorf("stage files: %w", err)
	}

	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	if status.IsClean() {
		return nil, errors.New("nothing to publish — skill already matches registry")
	}
	if req.DryRun {
		return &PublishResult{
			Skill:    skill.Frontmatter.Name,
			Registry: regName,
			Branch:   branch,
			Tag:      req.Tag,
			DryRun:   true,
		}, nil
	}

	message := req.Message
	if message == "" {
		message = fmt.Sprintf("Publish %s", skill.Frontmatter.Name)
	}
	author := req.Author
	if author == "" {
		author = "quiver"
	}
	authorEmail := req.AuthorEmail
	if authorEmail == "" {
		authorEmail = "quiver@localhost"
	}

	commit, err := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{Name: author, Email: authorEmail, When: time.Now()},
	})
	if err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	refSpecs := []string{
		fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch),
	}

	// Create the tag locally before pushing so branch + tag go upstream in one
	// atomic push. If the tag push fails we roll back the local tag to avoid
	// leaving the working repo in a half-released state.
	if req.Tag != "" {
		tagRef := plumbing.NewTagReferenceName(req.Tag)
		if _, err := repo.Reference(tagRef, false); err == nil {
			return nil, fmt.Errorf("tag %s already exists on the target registry", req.Tag)
		}
		if _, err := repo.CreateTag(req.Tag, commit, &gogit.CreateTagOptions{
			Tagger: &object.Signature{
				Name:  author,
				Email: authorEmail,
				When:  time.Now(),
			},
			Message: fmt.Sprintf("Release %s", req.Tag),
		}); err != nil {
			return nil, fmt.Errorf("create tag: %w", err)
		}
		refSpecs = append(refSpecs, fmt.Sprintf("refs/tags/%s:refs/tags/%s", req.Tag, req.Tag))
	}

	if err := p.Git.Push(ctx, stageDir, "origin", refSpecs); err != nil {
		if req.Tag != "" {
			_ = repo.DeleteTag(req.Tag)
		}
		return nil, fmt.Errorf("push: %w", err)
	}

	// Nudge the bare registry cache so subsequent installs see the new commit.
	_ = p.Git.Fetch(ctx, barePath)

	return &PublishResult{
		Skill:    skill.Frontmatter.Name,
		Registry: regName,
		Branch:   branch,
		Tag:      req.Tag,
		Commit:   commit.String(),
	}, nil
}

// checkoutPublishBranch puts the worktree on branch, creating a local
// tracking branch from refs/remotes/origin/<branch> if needed. If the
// branch is absent on origin and autoCreateFrom is non-empty, the branch
// is planted at that ref's tip instead of erroring — the user's
// "publish to a new branch" flow then works without pre-creating the
// branch with raw git.
func checkoutPublishBranch(repo *gogit.Repository, wt *gogit.Worktree, branch, autoCreateFrom string) error {
	if branch == "" {
		return nil
	}
	head, err := repo.Head()
	if err == nil && head.Name().IsBranch() && head.Name().Short() == branch {
		return nil
	}
	localRef := plumbing.NewBranchReferenceName(branch)
	if _, err := repo.Reference(localRef, false); err == nil {
		return wt.Checkout(&gogit.CheckoutOptions{Branch: localRef})
	}
	remoteRef := plumbing.NewRemoteReferenceName("origin", branch)
	if rr, err := repo.Reference(remoteRef, true); err == nil {
		if err := repo.Storer.SetReference(plumbing.NewHashReference(localRef, rr.Hash())); err != nil {
			return fmt.Errorf("create local branch: %w", err)
		}
		return wt.Checkout(&gogit.CheckoutOptions{Branch: localRef})
	}
	if autoCreateFrom == "" {
		return fmt.Errorf("branch %q not found on origin", branch)
	}
	sourceHash, err := repo.ResolveRevision(plumbing.Revision(autoCreateFrom))
	if err != nil || sourceHash == nil {
		return fmt.Errorf("cannot auto-create branch %q: source %q not found", branch, autoCreateFrom)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(localRef, *sourceHash)); err != nil {
		return fmt.Errorf("create local branch: %w", err)
	}
	return wt.Checkout(&gogit.CheckoutOptions{Branch: localRef})
}

// copyDir recursively copies src → dst, skipping .git and common OS noise.
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		base := filepath.Base(rel)
		if strings.HasPrefix(rel, ".git") || base == ".DS_Store" || base == "Thumbs.db" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
