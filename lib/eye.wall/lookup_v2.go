/*-
 * Copyright © 2018, 1&1 Internet SE
 * All rights reserved.
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package wall // import "github.com/solnx/eye/lib/eye.wall"

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-resty/resty"
	"github.com/solnx/eye/lib/eye.proto/v2"
)

// v2LookupEye queries the Eye monitoring profile server
func (l *Lookup) v2LookupEye(lookID string) (*v2.Result, error) {
	var err error
	var resp *resty.Response
	var result *v2.Result
	if resp, err = l.client.R().
		SetPathParams(map[string]string{
			`lookID`: lookID,
		}).Get(
		l.eyeLookupURL,
	); err != nil {
		return nil, fmt.Errorf("eyewall.Lookup: %s", err.Error())
	}

	switch resp.StatusCode() {
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("eyewall.Lookup: %s", resp.String())
	}

	result, err = v2Result(resp.Body())
	switch err {
	case nil:
		// success
		return result, nil
	case ErrUnconfigured:
		// no profiles for lookID
		go l.setUnconfigured(lookID)
		return nil, ErrUnconfigured
	default:
		return nil, fmt.Errorf("eyewall.Lookup: %s", err.Error())
	}
}

// v2Process converts t into Threshold and stores it in the
// local cache if available
func (l *Lookup) v2Process(lookID string, pr *v2.Result) (map[string]Threshold, error) {
	if pr.Configurations == nil {
		return nil, fmt.Errorf(`eyewall.Lookup: v2Process received pr.Configurations == nil`)
	}
	if len(*pr.Configurations) == 0 {
		go l.setUnconfigured(lookID)
		return nil, ErrUnconfigured
	}

	res := make(map[string]Threshold)
	for _, i := range *pr.Configurations {
		t := Threshold{
			ID:             i.ID,
			Metric:         i.Metric,
			HostID:         i.HostID,
			Oncall:         i.Data[0].Oncall,
			Interval:       i.Data[0].Interval,
			MetaMonitoring: i.Data[0].Monitoring,
			MetaTeam:       i.Data[0].Team,
			MetaSource:     i.Data[0].Source,
			MetaTargethost: i.Data[0].Targethost,
		}
		l.v2UpdateCachedActivation(i.ID, i.ActivatedAt)

		t.Thresholds = make(map[string]float64)
		for _, tl := range i.Data[0].Thresholds {
			lvl := strconv.FormatUint(uint64(tl.Level), 10)
			t.Predicate = tl.Predicate
			t.Thresholds[lvl] = tl.Value
		}

		go l.storeThreshold(lookID, &t)
		res[t.ID] = t
	}
	return res, nil
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
