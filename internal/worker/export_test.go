// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package worker

// Parse reads the pull request fields for table tests.
func Parse(body []byte) (string, string, error) {
	return parse(body)
}
