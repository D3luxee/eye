/*-
 * Copyright (c) 2018, 1&1 Internet SE
 * All rights reserved
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package rest // import "github.com/mjolnir42/eye/internal/eye.rest"

import (
	"encoding/json"
	"net/http"

	msg "github.com/mjolnir42/eye/internal/eye.msg"
	"github.com/mjolnir42/eye/lib/eye.proto/v2"
)

// respond is the output function for all requests
func respond(w *http.ResponseWriter, r *msg.Result) {
	switch r.Version {
	case msg.ProtocolInvalid:
		panic(`API Protocol 0 is not valid`)
	case msg.ProtocolOne:
		respondV1(w, r)
	case msg.ProtocolTwo:
		respondV2(w, r)
	}
}

// respondV1 is the output function emitting API version 1 results
func respondV1(w *http.ResponseWriter, r *msg.Result) {
	var bjson []byte
	var err error

	switch r.Section {
	case msg.SectionRegistration:
		panic(`API Protocol 1 does not have registrations`)
	}

	switch r.Section {
	case msg.SectionConfiguration:
		switch r.Action {
		case msg.ActionList:
			code, errstr, list := r.ExportV1ConfigurationList()
			if bjson, err = json.Marshal(&list); err != nil {
				hardInternalError(w)
				return
			}
			sendV1Result(w, code, errstr, &bjson)
			return
		case msg.ActionRemove:
			if r.Error != nil && r.Code >= msg.ResultServerError {
				http.Error(*w, r.Error.Error(), http.StatusInternalServerError)
				return
			} else if r.Error != nil && r.Code >= msg.ResultBadRequest {
				http.Error(*w, r.Error.Error(), http.StatusBadRequest)
				return
			}
			(*w).WriteHeader(http.StatusNoContent)
			(*w).Write(nil)
			return
		}
	}
}

// respondV2 is the output function emitting API version 2 results
func respondV2(w *http.ResponseWriter, r *msg.Result) {
	var (
		bjson    []byte
		err      error
		feedback string
		protoRes v2.Result
	)

	// create external protocol result
	switch r.Section {
	case msg.SectionConfiguration:
		protoRes = v2.NewConfigurationResult()
	case msg.SectionDeployment:
		protoRes = v2.NewConfigurationResult()
	case msg.SectionLookup:
		protoRes = v2.NewConfigurationResult()
	case msg.SectionRegistration:
		protoRes = v2.NewRegistrationResult()
	}
	feedback = `success`
	// record what was performed
	protoRes.Section = r.Section
	protoRes.Action = r.Action

	// internal result contains an error, copy over into protocol result
	if r.Error != nil {
		*protoRes.Errors = append(*protoRes.Errors, r.Error.Error())
		feedback = `failed`
	}

	// copy internal result data into protocol result
	switch r.Section {
	case msg.SectionConfiguration:
		*protoRes.Configurations = append(*protoRes.Configurations, r.Configuration...)
	case msg.SectionDeployment:
		*protoRes.Configurations = append(*protoRes.Configurations, r.Configuration...)
	case msg.SectionLookup:
		*protoRes.Configurations = append(*protoRes.Configurations, r.Configuration...)
	case msg.SectionRegistration:
		*protoRes.Registrations = append(*protoRes.Registrations, r.Registration...)
	}

	// trigger omitempty JSON encoding conditions if applicable
	if protoRes.Configurations != nil && len(*protoRes.Configurations) == 0 {
		*protoRes.Configurations = nil
	}
	if protoRes.Registrations != nil && len(*protoRes.Registrations) == 0 {
		*protoRes.Registrations = nil
	}

	// set protocol result status
	protoRes.SetStatus(r.Code)

	switch {
	// no results are exported on error to avoid accidental data leaks
	// no cache invalidation for failed requests
	// no alarm clearing for failed requests
	case r.Code >= 400:
		*protoRes.Configurations = nil
		*protoRes.Registrations = nil
		r.Flags.CacheInvalidation = false
		r.Flags.AlarmClearing = false
		feedback = `failed`
	}

	// send deployment feedback to SOMA
	if r.Flags.SendDeploymentFeedback {
		go sendSomaFeedback(r.FeedbackURL, feedback)
	}

	if r.Flags.CacheInvalidation && !r.Flags.AlarmClearing {
		// TODO: asynchronous active cache invalidation, since no
		// clearing action depends on the invalidation having been
		// performed
	}

	if r.Flags.CacheInvalidation && r.Flags.AlarmClearing {
		// TODO:  synchronous active cache invalidation, since the
		// clearing has to be blocked until the invalidation has been
		// performed
	}

	// send OK event to CAMS to clear alarmseries
	if r.Flags.AlarmClearing {
		go clearCamsAlarm(r)
	}

	if bjson, err = json.Marshal(&protoRes); err != nil {
		hardInternalError(w)
		return
	}

	sendJSONReply(w, &bjson)
	return
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
