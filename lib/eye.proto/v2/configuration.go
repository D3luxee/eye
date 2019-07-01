/*-
 * Copyright © 2018, 1&1 Internet SE
 * All rights reserved.
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package v2 // import "github.com/solnx/eye/lib/eye.proto/v2"

import (
	"time"

	"github.com/solnx/eye/lib/eye.proto/v1"
)

// Configuration holds the monitoring profile definition for a check
// that has to be performed
type Configuration struct {
	ActivatedAt string `json:"activatedAt"`
	Data        []Data `json:"data"`
	HostID      uint64 `json:"hostID,string"`
	Hostname    string `json:"hostname"`
	ID          string `json:"configurationID" valid:"uuidv4"`
	LookupID    string `json:"lookupID"`
	Metric      string `json:"metric"`
}

// Data contains a configuration
type Data struct {
	ID         string          `json:"dataID" valid:"uuidv4"`
	Info       MetaInformation `json:"information"`
	Interval   uint64          `json:"interval"`
	Monitoring string          `json:"monitoring"`
	Oncall     string          `json:"oncall"`
	Source     string          `json:"source"`
	Tags       []string        `json:"tags,omitempty"`
	Targethost string          `json:"targethost" valid:"host"`
	Team       string          `json:"string"`
	Thresholds []Threshold     `json:"thresholds"`
}

// Threshold contains the specification for a threshold of
// a Configuration
type Threshold struct {
	Predicate string  `json:"predicate"`
	Level     uint16  `json:"level"`
	Value     float64 `json:"value"`
}

// MetaInformation contains registration metadata for the Configuration
type MetaInformation struct {
	ValidFrom       string   `json:"validFrom"`
	ValidUntil      string   `json:"validUntil"`
	ProvisionedAt   string   `json:"provisionedAt"`
	DeprovisionedAt string   `json:"deprovisionedAt"`
	Tasks           []string `json:"tasks"`
}

// Snapshot is a Configuration at a specific point in time
type Snapshot struct {
	Valid      bool
	ValidAt    string
	CurrentTS  string
	HostID     uint64
	Hostname   string
	ID         string
	LookupID   string
	Metric     string
	Interval   uint64
	Monitoring string
	Oncall     string
	Source     string
	Tags       []string
	Targethost string
	Team       string
	Thresholds []Threshold
}

// NewConfigurationRequest returns a new request
func NewConfigurationRequest() Request {
	return Request{
		Flags:         &Flags{},
		Configuration: &Configuration{},
	}
}

// NewConfigurationResult returns a new result
func NewConfigurationResult() Result {
	return Result{
		Errors:         &[]string{},
		Configurations: &[]Configuration{},
	}
}

// InputSanatize ensures metadata fields are empty for c
func (c *Configuration) InputSanatize() {
	c.ActivatedAt = ``
	c.LookupID = ``
	for i, data := range c.Data {
		data.ID = ``
		data.Info = MetaInformation{}
		c.Data[i] = data
	}
}

// At returns the configuration of c that was valid at ts with s.valid
// set to true. If c contains no configuration data that was valid at ts
// then s.valid is false.
func (c *Configuration) At(ts time.Time) (s *Snapshot) {
	s = &Snapshot{}

dataloop:
	for i := range c.Data {
		if !c.Data[i].validate(ts) {
			continue dataloop
		}
		s.Valid = true
		s.ValidAt = ts.UTC().Format(TimeFormatString)
		s.CurrentTS = time.Now().UTC().Format(TimeFormatString)
		s.HostID = c.HostID
		s.Hostname = c.Hostname
		s.ID = c.ID
		s.LookupID = c.LookupID
		s.Metric = c.Metric
		s.Interval = c.Data[i].Interval
		s.Monitoring = c.Data[i].Monitoring
		s.Oncall = c.Data[i].Oncall
		s.Source = c.Data[i].Source
		s.Tags = c.Data[i].Tags
		s.Targethost = c.Data[i].Targethost
		s.Team = c.Data[i].Team
		s.Thresholds = c.Data[i].Thresholds
	}
	return
}

// Latest returns a snapshot of c using the latest data
func (c *Configuration) Latest() (s *Snapshot) {
	s = &Snapshot{
		CurrentTS: time.Now().UTC().Format(TimeFormatString),
		HostID:    c.HostID,
		Hostname:  c.Hostname,
		ID:        c.ID,
		LookupID:  c.LookupID,
		Metric:    c.Metric,
	}

	latest := NegTimeInf

dataloop:
	for i := range c.Data {
		validUntilTime := ParseValidity(c.Data[i].Info.ValidUntil)
		if !validUntilTime.After(latest) {
			continue dataloop
		}
		latest = validUntilTime
		s.Valid = true
		s.ValidAt = latest.UTC().Format(TimeFormatString)
		s.Interval = c.Data[i].Interval
		s.Monitoring = c.Data[i].Monitoring
		s.Oncall = c.Data[i].Oncall
		s.Source = c.Data[i].Source
		s.Tags = c.Data[i].Tags
		s.Targethost = c.Data[i].Targethost
		s.Team = c.Data[i].Team
		s.Thresholds = c.Data[i].Thresholds
	}
	return
}

// validate returns the evaluation result of the following condition:
//	d.Info.ValidFrom <= at <= d.Info.ValidUntil
func (d *Data) validate(at time.Time) bool {
	validFromTime := ParseValidity(d.Info.ValidFrom)
	validUntilTime := ParseValidity(d.Info.ValidUntil)

	return (at.UTC().Equal(validFromTime.UTC()) ||
		at.UTC().After(validFromTime.UTC())) &&
		(at.UTC().Equal(validUntilTime.UTC()) ||
			at.UTC().Before(validUntilTime.UTC()))
}

// ConfigurationFromV1 converts configuration data between protocol
// versions v1 and v2
func ConfigurationFromV1(item *v1.ConfigurationItem) Configuration {
	cfg := Configuration{
		HostID:   item.HostID,
		Hostname: item.Metadata.Targethost,
		ID:       item.ConfigurationItemID,
		Metric:   item.Metric,
	}
	data := Data{
		Interval:   item.Interval,
		Monitoring: item.Metadata.Monitoring,
		Oncall:     item.Oncall,
		Source:     item.Metadata.Source,
		Tags:       item.Tags,
		Targethost: item.Metadata.Targethost,
		Team:       item.Metadata.Team,
	}

	data.Thresholds = make([]Threshold, len(item.Thresholds))
	for i, thr := range item.Thresholds {
		data.Thresholds[i] = Threshold{
			Predicate: thr.Predicate,
			Level:     thr.Level,
			Value:     thr.Value,
		}
	}

	cfg.Data = []Data{
		data,
	}
	return cfg
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
