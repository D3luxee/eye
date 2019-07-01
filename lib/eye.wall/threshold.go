/*-
 * Copyright © 2016,2017, Jörg Pernfuß <code.jpe@gmail.com>
 * Copyright © 2016, 1&1 Internet SE
 * All rights reserved.
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package wall // import "github.com/solnx/eye/lib/eye.wall"

// Threshold is an internal datastructure for monitoring profile
// thresholds suitable for storage in the Cache
type Threshold struct {
	ID             string
	Metric         string
	HostID         uint64
	Oncall         string
	Interval       uint64
	MetaMonitoring string
	MetaTeam       string
	MetaSource     string
	MetaTargethost string
	Predicate      string
	Thresholds     map[string]float64
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
