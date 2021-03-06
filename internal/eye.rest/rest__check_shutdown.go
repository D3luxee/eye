/*-
 * Copyright (c) 2018, 1&1 Internet SE
 * All rights reserved
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package rest // import "github.com/solnx/eye/internal/eye.rest"

import (
	"net/http"
	"time"

	"github.com/julienschmidt/httprouter"
	metrics "github.com/rcrowley/go-metrics"
)

// CheckShutdown denies the request if a shutdown is in progress
func (x *Rest) CheckShutdown(h httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request,
		ps httprouter.Params) {
		if !ShutdownInProgress {
			metrics.GetOrRegisterCounter(`.requests`, Metrics[`eye`]).Inc(1)
			start := time.Now()

			h(w, r, ps)

			metrics.GetOrRegisterTimer(`.requests.latency`,
				Metrics[`soma`]).UpdateSince(start)
			return
		}

		http.Error(w, `Shutdown in progress`,
			http.StatusServiceUnavailable)
	}
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
