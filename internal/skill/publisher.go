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

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/registry"
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
	// Force allows the publish to overwrite an existing same-name skill in
	// the target registry. Without it, the publish refuses when
	// skills/<name>/ already exists on the target branch — protecting
	// against accidental or malicious overwrites (issue #72).
	Force bool
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
	// Capture whether the registry already holds a same-name skill so we
	// can distinguish "create new" from "overwrite existing" after the
	// status check. A no-content-change re-publish on an existing skill is
	// still a no-op ("nothing to publish") and proceeds without --force;
	// an actual overwrite requires explicit --force (issue #72).
	destPreExists := false
	if _, err := os.Stat(dest); err == nil {
		destPreExists = true
	}
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
	// Status is dirty AND the dest pre-existed → this is an overwrite, not
	// a create. Refuse without --force (issue #72).
	if destPreExists && !req.Force {
		return nil, fmt.Errorf("publish: registry %q already contains a different version of skill %q at %s — pass --force to overwrite",
			regName, skill.Frontmatter.Name, filepath.Join("skills", skill.Frontmatter.Name))
	}
	if req.DryRun {
		return &PublishResult{
			Skill:    skill.Frontmatter.Name,
			Registry: regName,
			Branch:   branch,
			Tag:      skillVersionTag(skill.Frontmatter.Name, req.Tag),
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
	//
	// Greenfield publish always lands the skill nested under skills/<name>/, so
	// the version tag is namespaced per skill (`<name>/<tag>`) — two skills in
	// one registry can both debut at the same semver without colliding on a
	// repo-global tag (issue #152). `qvr add <name>@<tag>` maps back to it
	// transparently via resolveSkillRef.
	tagName := skillVersionTag(skill.Frontmatter.Name, req.Tag)
	if tagName != "" {
		tagRef := plumbing.NewTagReferenceName(tagName)
		if _, err := repo.Reference(tagRef, false); err == nil {
			return nil, fmt.Errorf("tag %s already exists on the target registry — bump --tag for this skill", tagName)
		}
		if _, err := repo.CreateTag(tagName, commit, &gogit.CreateTagOptions{
			Tagger: &object.Signature{
				Name:  author,
				Email: authorEmail,
				When:  time.Now(),
			},
			Message: fmt.Sprintf("Release %s %s", skill.Frontmatter.Name, req.Tag),
		}); err != nil {
			return nil, fmt.Errorf("create tag: %w", err)
		}
		refSpecs = append(refSpecs, fmt.Sprintf("refs/tags/%s:refs/tags/%s", tagName, tagName))
	}

	if err := p.Git.Push(ctx, stageDir, "origin", refSpecs); err != nil {
		if tagName != "" {
			_ = repo.DeleteTag(tagName)
		}
		return nil, fmt.Errorf("push: %w", err)
	}

	// Nudge the bare registry cache so subsequent installs see the new commit.
	_ = p.Git.Fetch(ctx, barePath)

	return &PublishResult{
		Skill:    skill.Frontmatter.Name,
		Registry: regName,
		Branch:   branch,
		Tag:      tagName,
		Commit:   commit.String(),
	}, nil
}

// skillVersionTag returns the git tag for publishing skillName at tag into a
// nested (multi-skill) registry layout: the per-skill namespaced form
// "<skill>/<tag>" so two skills can share a semver without colliding on a
// repo-global tag (issue #152). An empty tag yields "" (no tag requested).
func skillVersionTag(skillName, tag string) string {
	if tag == "" {
		return ""
	}
	return skillName + model.SkillTagSep + tag
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
