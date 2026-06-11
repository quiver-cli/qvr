package discover

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// candidate is one session-store file found on disk, with the stat
// fingerprint the scan ledger diffs against.
type candidate struct {
	path    string
	size    int64
	mtimeMs int64
}

// enumerate walks one store's roots and returns every matching session file
// modified at/after since (zero since = no cutoff). Missing roots are normal
// (the agent isn't installed, or has no history yet) and contribute nothing.
func enumerate(st SessionStore, since time.Time) ([]candidate, error) {
	var out []candidate
	for _, root := range st.roots() {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		// WalkDir lstats its root, so a symlinked store dir would not descend;
		// resolve it first (agents and tests both use symlinked layouts).
		if resolved, rerr := filepath.EvalSymlinks(root); rerr == nil {
			root = resolved
		}
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return nil // unreadable subtree — skip, never fail the scan
			}
			if d.IsDir() {
				if !st.Recursive && path != root {
					return fs.SkipDir
				}
				return nil
			}
			if !st.matches(d.Name()) {
				return nil
			}
			fi, ferr := d.Info()
			if ferr != nil {
				return nil
			}
			if !since.IsZero() && fi.ModTime().Before(since) {
				return nil
			}
			out = append(out, candidate{
				path:    path,
				size:    fi.Size(),
				mtimeMs: fi.ModTime().UnixMilli(),
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
