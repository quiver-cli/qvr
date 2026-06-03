package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// LockSentinelSuffix is the suffix used for flock sentinel files.
const LockSentinelSuffix = ".flock"

// locksDir is where per-lockfile flock sentinels live, under quiver home. They
// are kept here rather than beside the lock file for two reasons:
//
//  1. Cleanliness: project directories stay free of qvr bookkeeping files. This
//     mirrors uv, which keeps its advisory install locks under its cache dir
//     (~/.cache/uv) rather than littering your project alongside uv.lock.
//  2. Correctness: LockFile.Write replaces the lock file via atomic tmp+rename.
//     A flock held on qvr.lock itself would be orphaned the instant the inode
//     is swapped, so the sentinel must be a stable file that is never renamed.
func locksDir(quiverHome string) string {
	return filepath.Join(quiverHome, "locks")
}

// lockSentinelPath maps a lock file path to its sentinel under quiver home,
// keyed by a hash of the lock file's absolute path. Distinct projects hash to
// distinct sentinels (no false contention); the same lock file always maps to
// the same sentinel regardless of how its path was expressed (relative vs
// absolute), so concurrent callers in one project still serialise.
func lockSentinelPath(quiverHome, lockPath string) string {
	abs, err := filepath.Abs(lockPath)
	if err != nil {
		abs = lockPath
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return filepath.Join(locksDir(quiverHome), hex.EncodeToString(sum[:])+LockSentinelSuffix)
}

// WithLock acquires an exclusive, blocking flock on the sentinel for lockPath
// (stored under quiverHome/locks), runs fn, then releases. Concurrent callers
// serialise — the second writer waits for the first to finish its
// read-modify-write before observing the lock file. This matches uv's
// behaviour for uv.lock and fixes the last-writer-wins race documented in
// issue #55, where parallel `qvr add` invocations would all report success but
// only the last writer's lockfile entry would survive.
//
// Callers should perform the entire read-modify-write inside fn:
//
//	model.WithLock(config.Dir(), lockPath, func() error {
//	    lock, err := model.ReadLockFile(lockPath)
//	    if err != nil { return err }
//	    lock.Put(entry)
//	    return lock.Write()
//	})
func WithLock(quiverHome, lockPath string, fn func() error) error {
	if lockPath == "" {
		return fmt.Errorf("lock path is empty")
	}
	if quiverHome == "" {
		return fmt.Errorf("quiver home is empty")
	}
	// Ensure both the lock file's project dir (fn writes the lock file there)
	// and the sentinel dir exist.
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("create lock dir: %w", err)
	}
	sentinel := lockSentinelPath(quiverHome, lockPath)
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o755); err != nil {
		return fmt.Errorf("create sentinel dir: %w", err)
	}
	fl := flock.New(sentinel)
	if err := fl.Lock(); err != nil {
		return fmt.Errorf("acquire %s: %w", filepath.Base(sentinel), err)
	}
	defer func() { _ = fl.Unlock() }()
	return fn()
}

// WithPublishLock acquires an exclusive, blocking flock at
// <quiverHome>/.qvr.lock.flock
// for the duration of fn. Unlike WithLock (which is keyed by a specific lock
// file path), this is a single user-machine-wide gate for any publish — so
// two concurrent `qvr publish` invocations (greenfield or installed, same
// project or different) serialise on this sentinel rather than racing on the
// remote registry's atomic ref check. Issue #88.
//
// Callers should wrap the ENTIRE publish — clone, commit, push, and any
// post-push lockfile updates — inside fn. Releasing before the push lands
// re-introduces the race the lock is meant to prevent.
func WithPublishLock(quiverHome string, fn func() error) error {
	if quiverHome == "" {
		return fmt.Errorf("quiver home is empty")
	}
	if err := os.MkdirAll(quiverHome, 0o755); err != nil {
		return fmt.Errorf("create quiver home: %w", err)
	}
	gatePath := filepath.Join(locksDir(quiverHome), "publish"+LockSentinelSuffix)
	if err := os.MkdirAll(filepath.Dir(gatePath), 0o755); err != nil {
		return fmt.Errorf("create sentinel dir: %w", err)
	}
	fl := flock.New(gatePath)
	if err := fl.Lock(); err != nil {
		return fmt.Errorf("acquire publish lock %s: %w", gatePath, err)
	}
	defer func() { _ = fl.Unlock() }()
	return fn()
}
