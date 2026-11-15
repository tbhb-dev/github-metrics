// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package store

import "time"

// OpenAt opens the store with a test clock.
func OpenAt(path string, now func() time.Time) (*Bolt, error) {
	return open(path, now)
}
