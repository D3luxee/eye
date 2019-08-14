/*-
 * Copyright (c) 2016, Jörg Pernfuß <code.jpe@gmail.com>
 * Copyright (c) 2018, 1&1 Internet SE
 * All rights reserved
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package eye // import "github.com/solnx/eye/internal/eye"

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Sirupsen/logrus"
	uuid "github.com/satori/go.uuid"
	msg "github.com/solnx/eye/internal/eye.msg"
	"github.com/solnx/eye/lib/eye.proto/v2"
)

// ConfigurationWrite handles write requests for configurations
type ConfigurationWrite struct {
	Input                       chan msg.Request
	Shutdown                    chan struct{}
	conn                        *sql.DB
	stmtLookupAddID             *sql.Stmt
	stmtCfgAddID                *sql.Stmt
	stmtCfgSelectValidForUpdate *sql.Stmt
	stmtCfgDataUpdateValidity   *sql.Stmt
	stmtCfgAddData              *sql.Stmt
	stmtProvAdd                 *sql.Stmt
	stmtActivationGet           *sql.Stmt
	stmtProvFinalize            *sql.Stmt
	stmtActivationDel           *sql.Stmt
	stmtCfgShow                 *sql.Stmt
	stmtActivationSet           *sql.Stmt
	appLog                      *logrus.Logger
}

// newConfigurationWrite return a new ConfigurationWrite handler with input buffer of length
func newConfigurationWrite(length int, appLog *logrus.Logger) (w *ConfigurationWrite) {
	w = &ConfigurationWrite{}
	w.Input = make(chan msg.Request, length)
	w.Shutdown = make(chan struct{})
	w.appLog = appLog
	return
}

// process is the request dispatcher called by Run
func (w *ConfigurationWrite) process(q *msg.Request) {
	result := msg.FromRequest(q)

	switch q.Action {
	case msg.ActionAdd:
		w.add(q, &result)
	case msg.ActionRemove:
		w.remove(q, &result)
	case msg.ActionUpdate:
		w.update(q, &result)
	case msg.ActionActivate:
		w.activate(q, &result)
	case msg.ActionNop:
		result.OK()
	default:
		result.UnknownRequest(q)
	}
	q.Reply <- result
}

// add inserts a configuration profile into the database
func (w *ConfigurationWrite) add(q *msg.Request, mr *msg.Result) {
	var (
		err                    error
		ok                     bool
		tx                     *sql.Tx
		jsonb                  []byte
		res                    sql.Result
		dataID                 string
		data                   v2.Data
		rolloutTS, activatedAt time.Time
		skipInvalidatePrevious bool
		previous               v2.Configuration
	)
	Section := "Configuration"
	Action := "Add"
	// fully populate Configuration before JSON encoding it
	rolloutTS = time.Now().UTC()
	dataID = uuid.Must(uuid.NewV4()).String()
	q.Configuration.ActivatedAt = `unknown`

	data = q.Configuration.Data[0]
	data.ID = dataID
	data.Info = v2.MetaInformation{}
	q.Configuration.Data = []v2.Data{data}

	if jsonb, err = json.Marshal(q.Configuration); err != nil {
		w.appLog.Errorf("Section=%s Action=%s Error=%s", Section, Action, err.Error())
		mr.ServerError(err)
		return
	}

	if tx, err = w.conn.Begin(); err != nil {
		w.appLog.Errorf("Section=%s Action=%s Error=%s", Section, Action, err.Error())
		mr.ServerError(err)
		return
	}

	// Register lookup hash
	if res, err = tx.Stmt(w.stmtLookupAddID).Exec(
		q.LookupHash,
		int(q.Configuration.HostID),
		q.Configuration.Hostname,
		q.Configuration.Metric,
	); err != nil {
		goto abort
	}
	if !mr.ExpectedRows(&res, 0, 1) {
		goto rollback
	}

	// Register configurationID with its lookup hash
	if res, err = tx.Stmt(w.stmtCfgAddID).Exec(
		q.Configuration.ID,
		q.LookupHash,
	); err != nil {
		goto abort
	}
	if !mr.ExpectedRows(&res, 0, 1) {
		goto rollback
	}

	// since SOMA sends deprovision+rollout instead of update requests
	// so downstream consumers can be stateless, this creates a gap
	// between deprovision and rollout where an eye client could cache
	// the incorrect information that there is no configuration.
	// To bridge this gap, ConfigurationWrite.remove invalidates
	// configurations 15 minutes into the future.
	// For this reason there could be a (still valid) previous
	// configuration.
	if err = w.txCfgLoadActive(tx, q, &previous, w.appLog); err == sql.ErrNoRows {
		// no still valid data is a non-error state, the 15minutes could
		// have expired or this is the first rollout
		skipInvalidatePrevious = true
	} else if err != nil {
		goto abort
	}

	// update validity data for previous configuration if found
	if !skipInvalidatePrevious {
		if ok, err = w.txSetDataValidity(tx, mr,
			v2.ParseValidity(previous.Data[0].Info.ValidFrom),
			rolloutTS,
			previous.Data[0].ID,
		); err != nil {
			goto abort
		} else if !ok {
			goto rollback
		}
	}

	// insert configuration data as valid from rolloutTS to infinity
	// and record provision request
	if ok, err = w.txInsertCfgData(tx, mr,
		dataID,
		q.Configuration.ID,
		rolloutTS,
		jsonb,
	); err != nil {
		goto abort
	} else if !ok {
		goto rollback
	}

	// query if this configurationID is activated
	if err = tx.Stmt(w.stmtActivationGet).QueryRow(
		q.Configuration.ID,
	).Scan(
		&activatedAt,
	); err == sql.ErrNoRows {
		q.Configuration.ActivatedAt = `never`
	} else if err != nil {
		goto abort
	} else {
		q.Configuration.ActivatedAt = activatedAt.Format(RFC3339Milli)
	}

	if err = tx.Commit(); err != nil {
		w.appLog.Errorf("Section=%s Action=%s Error=%s", Section, Action, err.Error())
		mr.ServerError(err)
		return
	}

	// generate full reply
	data.Info = v2.MetaInformation{
		ValidFrom:       v2.FormatValidity(rolloutTS),
		ValidUntil:      `forever`,
		ProvisionedAt:   v2.FormatValidity(rolloutTS),
		DeprovisionedAt: `never`,
		Tasks:           []string{msg.TaskRollout},
	}
	q.Configuration.Data = []v2.Data{data}
	mr.Configuration = append(mr.Configuration, q.Configuration)
	mr.OK()
	return

abort:
	w.appLog.Debugf("Section=%s Action=%s Error=%s", Section, Action, err.Error())
	mr.ServerError(err)

rollback:
	if mr.Error == nil {
		mr.ServerError(fmt.Errorf("transaction aborted"))
	}
	tx.Rollback()
}

// remove deletes a configuration from the database
func (w *ConfigurationWrite) remove(q *msg.Request, mr *msg.Result) {
	var (
		err                       error
		ok                        bool
		tx                        *sql.Tx
		res                       sql.Result
		task                      string
		transactionTS, validUntil time.Time
		configuration             v2.Configuration
		data                      v2.Data
	)
	Section := "Configuration"
	Action := "Remove"
	transactionTS = time.Now().UTC()

	// deprovision requests have a 15 minute grace window to send the
	// new configuration data
	task = msg.TaskDeprovision
	validUntil = transactionTS.Add(15 * time.Minute)

	// for final deletions, no 15 minute grace period for updates is
	// required or granted
	if q.ConfigurationTask == msg.TaskDelete {
		validUntil = transactionTS
		task = msg.TaskDelete
	}

	// record that this request had the clearing flag set
	if task == msg.TaskDeprovision && q.Flags.AlarmClearing {
		task = msg.TaskClearing
	}

	// open transaction
	if tx, err = w.conn.Begin(); err != nil {
		w.appLog.Errorf("Section=%s Action=%s Error=%s", Section, Action, err.Error())
		mr.ServerError(err)
		return
	}

	// check an active version of this configuration exists, then load
	// it; this is required for requests with q.Flags.AlarmClearing set
	// to true so that the OK event can be constructed with the correct
	// metadata
	if err = w.txCfgLoadActive(tx, q, &configuration, w.appLog); err == sql.ErrNoRows {
		// there is no active configuration that can be loaded for clearing or invalidation
		mr.Flags.AlarmClearing = false
		mr.Flags.CacheInvalidation = false
		//but there could still be an configuration which has to be deleted
		if &configuration == nil || len(configuration.Data) == 0 {
			//there was no configuration, this is a noop
			goto commitTx
		}
	} else if err != nil {
		goto abort
	}

	// XXX
	data = configuration.Data[0]

	// it is entirely possible that the configuration data is about to
	// expire just as this transaction is running. If the loaded validUntil is
	// not positive infinity then it is kept as is since the
	// configuration is already expiring
	if !msg.PosTimeInf.Equal(v2.ParseValidity(data.Info.ValidUntil)) {
		validUntil = v2.ParseValidity(data.Info.ValidUntil)
	}

	// if there is already an earlier deprovisioning timestamp it is left in
	// place and backdate this transaction
	if v2.ParseProvision(data.Info.DeprovisionedAt).Before(transactionTS) {
		transactionTS = v2.ParseProvision(data.Info.DeprovisionedAt)
	}
	data.Info.ValidUntil = v2.FormatValidity(validUntil)
	data.Info.DeprovisionedAt = v2.FormatProvision(transactionTS)
	configuration.Data[0] = data

	mr.Configuration = append(mr.Configuration, configuration)

	// update validity records within the database
	if ok, err = w.txSetDataValidity(tx, mr,
		v2.ParseValidity(data.Info.ValidFrom),
		v2.ParseValidity(data.Info.ValidUntil),
		data.ID,
	); err != nil {
		goto abort
	} else if !ok {
		goto rollback
	}

	// update provisioning record
	if ok, err = w.txFinalizeProvision(tx, mr,
		transactionTS,
		data.ID,
		task,
	); err != nil {
		goto abort
	} else if !ok {
		goto rollback
	}

	// remove the metric activation if required
	if q.Flags.ResetActivation {
		if res, err = tx.Stmt(w.stmtActivationDel).Exec(
			q.Configuration.ID,
		); err != nil {
			goto abort
		}
		// 0: activation reset on inactive configurations is valid
		if !mr.ExpectedRows(&res, 0, 1) {
			goto rollback
		}
	}

commitTx:
	if err = tx.Commit(); err != nil {
		w.appLog.Errorf("Section=%s Action=%s Error=%s", Section, Action, err.Error())
		mr.ServerError(err)
		return
	}
	mr.OK()
	return

abort:
	w.appLog.Debugf("Section=%s Action=%s Error=%s", Section, Action, err.Error())
	mr.ServerError(err)
rollback:
	if mr.Error == nil {
		mr.ServerError(fmt.Errorf("transaction aborted"))
	}
	tx.Rollback()
}

// update replaces a configuration's data section with a new version
func (w *ConfigurationWrite) update(q *msg.Request, mr *msg.Result) {
	var (
		err error
		tx  *sql.Tx
		ok  bool

		jsonb          []byte
		transactionTS  time.Time
		prevCfg        v2.Configuration
		data, prevData v2.Data
	)
	Section := "Configuration"
	Action := "Update"
	transactionTS = time.Now().UTC()

	if tx, err = w.conn.Begin(); err != nil {
		w.appLog.Errorf("Section=%s Action=%s Error=%s", Section, Action, err.Error())
		mr.ServerError(err)
		return
	}

	// load the current configuration. for Update requests, there must
	// be currently valid data that is being updated
	if err = w.txCfgLoadActive(tx, q, &prevCfg, w.appLog); err == sql.ErrNoRows {
		// that which does not exist can not be updated
		if &prevCfg != nil && len(prevCfg.Data) > 0 {
			err = nil
		}
	} else if err != nil {
		w.appLog.Errorf("Section=%s Action1=%s ID=%s Error=%s", Section, Action, q.Configuration.ID, "txCfgLoadMatch was not ok")
		goto abort
	}
	if err == nil {

		//there is a old configuration which needs an update
		// update current data
		prevData = prevCfg.Data[0]
		prevTS := transactionTS.Truncate(1 * time.Second)
		// update validity of current data
		prevData.Info.ValidUntil = v2.FormatValidity(prevTS)
		if ok, err = w.txSetDataValidity(tx, mr,
			v2.ParseValidity(prevData.Info.ValidFrom),
			prevTS,
			prevData.ID,
		); err != nil {
			w.appLog.Errorf("Section=%s Action2=%s ID=%s Error=%s", Section, Action, q.Configuration.ID, err.Error())
			goto abort
		} else if !ok {
			w.appLog.Errorf("Section=%s Action2=%s ID=%s Error=%s", Section, Action, q.Configuration.ID, "txSetDataValidity was not ok")
			goto rollback
		}

		// update provisioning history of current data
		prevData.Info.DeprovisionedAt = v2.FormatProvision(prevTS)
		if ok, err = w.txFinalizeProvision(tx, mr,
			prevTS,
			prevData.ID,
			msg.TaskUpdate,
		); err != nil {
			w.appLog.Errorf("Section=%s Action3=%s ID=%s Error=%s", Section, Action, q.Configuration.ID, err.Error())
			goto abort
		} else if !ok {
			w.appLog.Errorf("Section=%s Action3=%s ID=%s Error=%s", Section, Action, q.Configuration.ID, "txFinalizeProvision was not ok")
			goto rollback
		}
	}
	// insert new data
	// always stored with ActivatedAt set to unknown inside the stored JSON
	q.Configuration.ActivatedAt = `unknown`

	data = q.Configuration.Data[0]
	data.ID = uuid.Must(uuid.NewV4()).String()
	data.Info = v2.MetaInformation{}
	q.Configuration.Data = []v2.Data{data}

	if jsonb, err = json.Marshal(q.Configuration); err != nil {
		goto abort
	}

	// insert configuration data as valid from transactionTS to infinity
	// and record provision request

	if ok, err = w.txInsertCfgData(tx, mr,
		data.ID,
		q.Configuration.ID,
		transactionTS,
		jsonb,
	); err != nil {
		w.appLog.Errorf("Section=%s Action4=%s ID=%s Error=%s", Section, Action, q.Configuration.ID, err.Error())
		goto abort
	} else if !ok {
		w.appLog.Errorf("Section=%s Action4=%s ID=%s Error=%s", Section, Action, q.Configuration.ID, "txInsertCfgData was not ok")
		goto rollback
	}

	// commit transaction
	if err = tx.Commit(); err != nil {
		mr.ServerError(err)
		return
	}

	// generate full reply
	data.Info = v2.MetaInformation{
		ValidFrom:       v2.FormatValidity(transactionTS),
		ValidUntil:      `forever`,
		ProvisionedAt:   v2.FormatValidity(transactionTS),
		DeprovisionedAt: `never`,
		Tasks:           []string{msg.TaskRollout},
	}

	// prevCfg has the populated ActivatedAt field
	if len(prevCfg.Data) == 0 {
		prevCfg.Data = []v2.Data{
			data,
		}
	} else {
		prevCfg.Data = []v2.Data{
			data,
			prevData,
		}
	}

	mr.Configuration = append(mr.Configuration, prevCfg)

	mr.OK()
	return

abort:
	w.appLog.Debugf("Section=%s Action=%s Error=%s", Section, Action, err.Error())
	mr.ServerError(err)

rollback:
	if mr.Error == nil {
		mr.ServerError(fmt.Errorf("transaction aborted"))
	}
	tx.Rollback()
}

// activate records a configuration activation
func (w *ConfigurationWrite) activate(q *msg.Request, mr *msg.Result) {
	var err error
	var res sql.Result
	Section := "Configuration"
	Action := "Activate"
	if res, err = w.stmtActivationSet.Exec(
		q.Configuration.ID,
	); err != nil {
		w.appLog.Errorf("Section=%s Action=%s Error=%s", Section, Action, err.Error())
		mr.ServerError(err)
		return
	}
	if mr.RowCnt(res.RowsAffected()) {
		mr.Configuration = append(mr.Configuration, q.Configuration)
	}
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
