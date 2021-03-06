// Copyright 2015 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lease

import (
	"io"
	"os"
	"sync"
)

// A sentinel error used when a lease has been revoked.
type RevokedError struct {
}

func (re *RevokedError) Error() string {
	return "Lease revoked"
}

// A read-only wrapper around a file that may be revoked, when e.g. there is
// temporary disk space pressure. A read lease may also be upgraded to a write
// lease, if it is still valid.
//
// All methods are safe for concurrent access.
type ReadLease interface {
	io.ReadSeeker
	io.ReaderAt

	// Return the size of the underlying file, or what the size used to be if the
	// lease has been revoked.
	Size() (size int64)

	// Has the lease been revoked? Note that this is completely racy in the
	// absence of external synchronization on all leases and the file leaser, so
	// is suitable only for testing purposes.
	Revoked() (revoked bool)

	// Attempt to upgrade the lease to a read/write lease. After successfully
	// upgrading, it is as if the lease has been revoked.
	Upgrade() (rwl ReadWriteLease, err error)

	// Cause the lease to be revoked and any associated resources to be cleaned
	// up, if it has not already been revoked.
	Revoke()
}

type readLease struct {
	// Used internally and by fileLeaser eviction logic.
	Mu sync.Mutex

	/////////////////////////
	// Constant data
	/////////////////////////

	size int64

	/////////////////////////
	// Dependencies
	/////////////////////////

	// The leaser that issued this lease.
	leaser *fileLeaser

	// The underlying file, set to nil once revoked.
	//
	// GUARDED_BY(Mu)
	file *os.File
}

var _ ReadLease = &readLease{}

func newReadLease(
	size int64,
	leaser *fileLeaser,
	file *os.File) (rl *readLease) {
	rl = &readLease{
		size:   size,
		leaser: leaser,
		file:   file,
	}

	return
}

////////////////////////////////////////////////////////////////////////
// Public interface
////////////////////////////////////////////////////////////////////////

// LOCKS_EXCLUDED(rl.Mu)
func (rl *readLease) Read(p []byte) (n int, err error) {
	rl.leaser.promoteToMostRecent(rl)

	rl.Mu.Lock()
	defer rl.Mu.Unlock()

	// Have we been revoked?
	if rl.revoked() {
		err = &RevokedError{}
		return
	}

	n, err = rl.file.Read(p)
	return
}

// LOCKS_EXCLUDED(rl.Mu)
func (rl *readLease) Seek(
	offset int64,
	whence int) (off int64, err error) {
	rl.Mu.Lock()
	defer rl.Mu.Unlock()

	// Have we been revoked?
	if rl.revoked() {
		err = &RevokedError{}
		return
	}

	off, err = rl.file.Seek(offset, whence)
	return
}

// LOCKS_EXCLUDED(rl.Mu)
func (rl *readLease) ReadAt(p []byte, off int64) (n int, err error) {
	rl.leaser.promoteToMostRecent(rl)

	rl.Mu.Lock()
	defer rl.Mu.Unlock()

	// Have we been revoked?
	if rl.revoked() {
		err = &RevokedError{}
		return
	}

	n, err = rl.file.ReadAt(p, off)
	return
}

// No lock necessary.
func (rl *readLease) Size() (size int64) {
	size = rl.size
	return
}

// LOCKS_EXCLUDED(rl.Mu)
func (rl *readLease) Revoked() (revoked bool) {
	rl.Mu.Lock()
	defer rl.Mu.Unlock()

	revoked = rl.revoked()
	return
}

// LOCKS_EXCLUDED(rl.leaser.mu)
// LOCKS_EXCLUDED(rl.Mu)
func (rl *readLease) Upgrade() (rwl ReadWriteLease, err error) {
	// Let the leaser do the heavy lifting.
	rwl, err = rl.leaser.upgrade(rl)
	return
}

// LOCKS_EXCLUDED(rl.leaser.mu)
// LOCKS_EXCLUDED(rl.Mu)
func (rl *readLease) Revoke() {
	// Let the leaser do the heavy lifting.
	rl.leaser.revokeVoluntarily(rl)
}

////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////

// Has the lease been revoked?
//
// LOCKS_REQUIRED(rl.Mu || rl.leaser.mu)
func (rl *readLease) revoked() bool {
	return rl.file == nil
}

// Relinquish control of the file, marking the lease as revoked.
//
// REQUIRES: Not yet revoked.
//
// LOCKS_REQUIRED(rl.Mu)
func (rl *readLease) release() (file *os.File) {
	if rl.revoked() {
		panic("Already revoked")
	}

	file = rl.file
	rl.file = nil

	return
}
