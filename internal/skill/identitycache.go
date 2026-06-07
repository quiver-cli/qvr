package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/registry"
)

// cachedIdentity is the persisted per-(commit, path, scope) identity record.
type cachedIdentity struct {
	SubtreeHash string `json:"subtreeHash"`
	TreeOID     string `json:"treeOID,omitempty"`
}

// identityCacheKey derives a stable filename key from the inputs that fully
// determine a subtree's canonical identity: the commit SHA (globally unique),
// the skill subpath, and the root-coexist scope flag (which selects scoped vs
// whole-subtree hashing). Any change in scope yields a different key, so a
// coexist hash can never be confused with a whole-repo hash for the same commit.
func identityCacheKey(commit, subpath string, rootCoexists bool) string {
	h := sha256.New()
	_, _ = h.Write([]byte(commit))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(subpath))
	_, _ = h.Write([]byte{0})
	if rootCoexists {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func identityCachePath(commit, subpath string, rootCoexists bool) string {
	key := identityCacheKey(commit, subpath, rootCoexists)
	return filepath.Join(registry.IdentityCacheRoot(), key[:2], key+".json")
}

// readCachedIdentity returns a previously computed (subtreeHash, treeOID) for an
// immutable (commit, subpath, scope), if present. A miss returns ok=false and
// the caller computes from the bare repo. Never returns a partial record.
func readCachedIdentity(commit, subpath string, rootCoexists bool) (subtreeHash, treeOID string, ok bool) {
	if commit == "" {
		return "", "", false
	}
	data, err := os.ReadFile(identityCachePath(commit, subpath, rootCoexists))
	if err != nil {
		return "", "", false
	}
	var ci cachedIdentity
	if jsonErr := json.Unmarshal(data, &ci); jsonErr != nil || ci.SubtreeHash == "" {
		return "", "", false
	}
	return ci.SubtreeHash, ci.TreeOID, true
}

// writeCachedIdentity records the identity for an immutable (commit, subpath,
// scope). Best-effort and concurrency-safe: the record is content-determined, so
// racing writers produce identical bytes; a temp-file + atomic rename keeps any
// reader from seeing a partial file. Failures are silently ignored — the cache
// is an optimization, never a correctness dependency.
func writeCachedIdentity(commit, subpath string, rootCoexists bool, subtreeHash, treeOID string) {
	if commit == "" || subtreeHash == "" {
		return
	}
	path := identityCachePath(commit, subpath, rootCoexists)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(cachedIdentity{SubtreeHash: subtreeHash, TreeOID: treeOID})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".id-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
	}
}

// cachedProvenance is the persisted per-(ref, commit, path) provenance + author
// record. HasProvenance distinguishes a cached nil provenance (git couldn't read
// the repo when first computed) from a cache miss.
type cachedProvenance struct {
	HasProvenance   bool   `json:"hasProvenance"`
	Provider        string `json:"provider,omitempty"`
	Tag             string `json:"tag,omitempty"`
	SignatureStatus string `json:"signatureStatus,omitempty"`
	Signer          string `json:"signer,omitempty"`
	Author          string `json:"author"`
}

func provenanceCacheKey(ref, commit, path string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(ref))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(commit))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(path))
	return hex.EncodeToString(h.Sum(nil))
}

func provenanceCachePath(ref, commit, path string) string {
	key := provenanceCacheKey(ref, commit, path)
	return filepath.Join(registry.ProvenanceCacheRoot(), key[:2], key+".json")
}

// readCachedProvenance returns a previously computed provenance + author for an
// immutable (ref, commit, path). ok=false on a miss. Callers MUST NOT use this
// for security-gated installs (require_signed / signed_by) — those re-verify
// signatures fresh, because signature status depends on the local keyring, which
// the cache does not capture.
func readCachedProvenance(ref, commit, path string) (prov *model.ProvenanceRef, author string, ok bool) {
	if commit == "" {
		return nil, "", false
	}
	data, err := os.ReadFile(provenanceCachePath(ref, commit, path))
	if err != nil {
		return nil, "", false
	}
	var cp cachedProvenance
	if jsonErr := json.Unmarshal(data, &cp); jsonErr != nil {
		return nil, "", false
	}
	if !cp.HasProvenance {
		return nil, cp.Author, true
	}
	return &model.ProvenanceRef{
		Provider:        cp.Provider,
		Tag:             cp.Tag,
		SignatureStatus: cp.SignatureStatus,
		Signer:          cp.Signer,
	}, cp.Author, true
}

// writeCachedProvenance records provenance + author for an immutable
// (ref, commit, path). Best-effort, content-determined, atomic-rename — same
// concurrency properties as writeCachedIdentity.
func writeCachedProvenance(ref, commit, path string, prov *model.ProvenanceRef, author string) {
	if commit == "" {
		return
	}
	rec := cachedProvenance{Author: author}
	if prov != nil {
		rec.HasProvenance = true
		rec.Provider = prov.Provider
		rec.Tag = prov.Tag
		rec.SignatureStatus = prov.SignatureStatus
		rec.Signer = prov.Signer
	}
	cachePath := provenanceCachePath(ref, commit, path)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(cachePath), ".prov-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, cachePath); err != nil {
		_ = os.Remove(tmpName)
	}
}

// resolveProvenanceAndAuthor returns the skill's git provenance and the author
// of its last-touching commit, using the global cache on the common path and
// recomputing fresh when fresh==true. fresh MUST be set for any install whose
// outcome depends on a current signature check (require_signed, or a skill that
// declares signed_by) — signature status is keyring-dependent and not safe to
// serve stale, whereas the author and the "invalid signature" tampering signal
// are content-determined and stable for an immutable commit.
func (in *Installer) resolveProvenanceAndAuthor(repoPath, ref, commit, path string, fresh bool) (*model.ProvenanceRef, string) {
	if !fresh {
		if prov, author, ok := readCachedProvenance(ref, commit, path); ok {
			return prov, author
		}
	}
	// Resolve the skill's last-touching commit once and thread it into both
	// provenance and author derivation — the public CheckGitProvenance and
	// CommitAuthor would otherwise each recompute it, spawning a redundant
	// `git log` on this cold path (#209/#203).
	skillCommit := SkillCommit(repoPath, commit, path)
	prov := checkGitProvenanceAt(repoPath, ref, skillCommit)
	author := commitAuthorAt(repoPath, skillCommit)
	if !fresh {
		writeCachedProvenance(ref, commit, path, prov, author)
	}
	return prov, author
}
