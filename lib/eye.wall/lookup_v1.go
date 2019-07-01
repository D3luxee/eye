/*-
 * Copyright © 2016,2017, Jörg Pernfuß <code.jpe@gmail.com>
 * Copyright © 2016, 1&1 Internet SE
 * All rights reserved.
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package wall // import "github.com/solnx/eye/lib/eye.wall"

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"

	"github.com/solnx/eye/lib/eye.proto/v1"
)

// v1LookupEye queries the Eye monitoring profile server
func (l *Lookup) v1LookupEye(lookID string) (*v1.ConfigurationData, error) {
	client := &http.Client{}
	req, err := http.NewRequest(`GET`, fmt.Sprintf(
		"http://%s:%s/%s/%s",
		l.Config.Eyewall.Host,
		l.Config.Eyewall.Port,
		l.Config.Eyewall.Path,
		lookID,
	), nil)
	if err != nil {
		return nil, err
	}

	var resp *http.Response
	defer resp.Body.Close()
	l.limit.Start()
	if resp, err = client.Do(req); err != nil {
		return nil, err
	} else if resp.StatusCode == 400 {
		return nil, fmt.Errorf(`Lookup: malformed LookupID`)
	} else if resp.StatusCode == 404 {
		go l.setUnconfigured(lookID)
		return nil, ErrUnconfigured
	} else if resp.StatusCode >= 500 {
		return nil, fmt.Errorf(
			"Lookup: server error from eye: %d",
			resp.StatusCode,
		)
	}
	l.limit.Done()
	var buf []byte
	buf, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	data := &v1.ConfigurationData{}
	err = json.Unmarshal(buf, data)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// v1Process converts t into Threshold and stores it in the
// local cache if available
func (l *Lookup) v1Process(lookID string, t *v1.ConfigurationData) (map[string]Threshold, error) {
	if t.Configurations == nil {
		return nil, fmt.Errorf(`lookup.process received t.Configurations == nil`)
	}
	if len(t.Configurations) == 0 {
		go l.setUnconfigured(lookID)
		return nil, ErrUnconfigured
	}
	res := make(map[string]Threshold)
	for _, i := range t.Configurations {
		t := Threshold{
			ID:             i.ConfigurationItemID,
			Metric:         i.Metric,
			HostID:         i.HostID,
			Oncall:         i.Oncall,
			Interval:       i.Interval,
			MetaMonitoring: i.Metadata.Monitoring,
			MetaTeam:       i.Metadata.Team,
			MetaSource:     i.Metadata.Source,
			MetaTargethost: i.Metadata.Targethost,
		}
		t.Thresholds = make(map[string]float64)
		for _, tl := range i.Thresholds {
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
