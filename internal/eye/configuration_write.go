/*-
 * Copyright (c) 2016, Jörg Pernfuß <code.jpe@gmail.com>
 * Copyright (c) 2018, 1&1 Internet SE
 * All rights reserved
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package eye // import "github.com/mjolnir42/eye/internal/eye"

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/lib/pq"
	msg "github.com/mjolnir42/eye/internal/eye.msg"
	"github.com/mjolnir42/eye/lib/eye.proto/v2"
	uuid "github.com/satori/go.uuid"
)

// ConfigurationWrite handles write requests for configurations
type ConfigurationWrite struct {
	Input                        chan msg.Request
	Shutdown                     chan struct{}
	conn                         *sql.DB
	stmtConfigurationUpdate      *sql.Stmt
	stmtLookupIDForConfiguration *sql.Stmt
	// NEW
	stmtNewLookupAdd            *sql.Stmt
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
	reqLog                      *logrus.Logger
	errLog                      *logrus.Logger
}

// newConfigurationWrite return a new ConfigurationWrite handler with input buffer of length
func newConfigurationWrite(length int) (w *ConfigurationWrite) {
	w = &ConfigurationWrite{}
	w.Input = make(chan msg.Request, length)
	w.Shutdown = make(chan struct{})
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
		err                               error
		tx                                *sql.Tx
		jsonb                             []byte
		res                               sql.Result
		dataID, previousDataID            string
		data                              v2.Data
		rolloutTS, validFrom, activatedAt time.Time
		skipInvalidatePrevious            bool
	)

	// fully populate Configuration before JSON encoding it
	rolloutTS = time.Now().UTC()
	dataID = uuid.Must(uuid.NewV4()).String()
	q.Configuration.LookupID = q.LookupHash
	q.Configuration.ActivatedAt = `unknown`

	data = q.Configuration.Data[0]
	data.ID = dataID
	data.Info = v2.MetaInformation{}
	q.Configuration.Data = []v2.Data{data}

	if jsonb, err = json.Marshal(q.Configuration); err != nil {
		mr.ServerError(err)
		return
	}

	if tx, err = w.conn.Begin(); err != nil {
		mr.ServerError(err)
		return
	}

	// Register lookup hash
	if res, err = tx.Stmt(w.stmtNewLookupAdd).Exec(
		q.LookupHash,
		int(q.Configuration.HostID),
		q.Configuration.Metric,
	); err != nil {
		mr.ServerError(err)
		tx.Rollback()
		return
	}
	if !mr.ExpectedRows(&res, 0, 1) {
		tx.Rollback()
		return
	}

	// Register configurationID with its lookup hash
	if res, err = tx.Stmt(w.stmtCfgAddID).Exec(
		q.Configuration.ID,
		q.LookupHash,
	); err != nil {
		mr.ServerError(err)
		tx.Rollback()
		return
	}
	if !mr.ExpectedRows(&res, 0, 1) {
		tx.Rollback()
		return
	}

	// database index ensures there is no overlap in validity ranges
	if err = tx.Stmt(w.stmtCfgSelectValidForUpdate).QueryRow(
		q.Configuration.ID,
	).Scan(
		&previousDataID,
		&validFrom,
	); err == sql.ErrNoRows {
		// no still valid data is a non-error state
		skipInvalidatePrevious = true
	} else if err != nil {
		mr.ServerError(err)
		tx.Rollback()
		return
	}

	if !skipInvalidatePrevious {
		if res, err = tx.Stmt(w.stmtCfgDataUpdateValidity).Exec(
			validFrom.Format(RFC3339Milli),
			rolloutTS.Format(RFC3339Milli),
			previousDataID,
		); err != nil {
			mr.ServerError(err)
			tx.Rollback()
			return
		}
		if !mr.ExpectedRows(&res, 1) {
			tx.Rollback()
			return
		}
	}

	// insert configuration data as valid from rolloutTS to infinity
	if res, err = tx.Stmt(w.stmtCfgAddData).Exec(
		dataID,
		q.Configuration.ID,
		rolloutTS.Format(RFC3339Milli),
		jsonb,
	); err != nil {
		mr.ServerError(err)
		tx.Rollback()
		return
	}
	if !mr.ExpectedRows(&res, 1) {
		tx.Rollback()
		return
	}

	// record provision request
	if _, err = tx.Stmt(w.stmtProvAdd).Exec(
		dataID,
		q.Configuration.ID,
		rolloutTS.Format(RFC3339Milli),
		pq.Array([]string{msg.TaskRollout}),
	); err != nil {
		mr.ServerError(err)
		tx.Rollback()
		return
	}
	if !mr.ExpectedRows(&res, 1) {
		tx.Rollback()
		return
	}

	// query if this configurationID is activated
	if err = tx.Stmt(w.stmtActivationGet).QueryRow(
		q.Configuration.ID,
	).Scan(
		&activatedAt,
	); err == sql.ErrNoRows {
		q.Configuration.ActivatedAt = `never`
	} else if err != nil {
		mr.ServerError(err)
		tx.Rollback()
		return
	} else {
		q.Configuration.ActivatedAt = activatedAt.Format(RFC3339Milli)
	}

	if err = tx.Commit(); err != nil {
		mr.ServerError(err)
		return
	}

	// generate full reply
	data.Info = v2.MetaInformation{
		ValidFrom:       rolloutTS.Format(RFC3339Milli),
		ValidUntil:      `infinity`,
		ProvisionedAt:   rolloutTS.Format(RFC3339Milli),
		DeprovisionedAt: `never`,
		Tasks:           []string{msg.TaskRollout},
	}
	q.Configuration.Data = []v2.Data{data}
	mr.Configuration = append(mr.Configuration, q.Configuration)
	mr.OK()
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
		mr.ServerError(err)
		return
	}

	// check an active version of this configuration exists, then load
	// it; this is required for requests with q.Flags.AlarmClearing set
	// to true so that the OK event can be constructed with the correct
	// metadata
	if err = w.txCfgLoadActive(tx, q, &configuration); err == sql.ErrNoRows {
		// that which does not exist can not be deleted
		goto commitTx
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
	if res, err = tx.Stmt(w.stmtProvFinalize).Exec(
		data.ID,
		transactionTS.Format(RFC3339Milli),
		task,
	); err != nil {
		goto abort
	}
	if !mr.ExpectedRows(&res, 1) {
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
		mr.ServerError(err)
		return
	}
	mr.OK()
	return

abort:
	mr.ServerError(err)

rollback:
	tx.Rollback()
}

// update replaces a configuration
func (w *ConfigurationWrite) update(q *msg.Request, mr *msg.Result) {
	var (
		err   error
		tx    *sql.Tx
		jsonb []byte
		res   sql.Result
	)

	if jsonb, err = json.Marshal(q.Configuration); err != nil {
		mr.ServerError(err)
		return
	}

	if tx, err = w.conn.Begin(); err != nil {
		mr.ServerError(err)
		return
	}

	if res, err = tx.Stmt(w.stmtConfigurationUpdate).Exec(
		q.Configuration.ID,
		q.LookupHash,
		jsonb,
	); err != nil {
		mr.ServerError(err)
		tx.Rollback()
		return
	}
	// statement should affect 1 row
	if count, _ := res.RowsAffected(); count != 1 {
		mr.ServerError(fmt.Errorf("Rollback: update statement affected %d rows", count))
		tx.Rollback()
		return
	}

	if err = tx.Commit(); err != nil {
		mr.ServerError(err)
		return
	}
	mr.OK()
}

// activate records a configuration activation
func (w *ConfigurationWrite) activate(q *msg.Request, mr *msg.Result) {
	var err error
	var res sql.Result

	if res, err = w.stmtActivationSet.Exec(
		q.Configuration.ID,
	); err != nil {
		mr.ServerError(err)
		return
	}
	if mr.RowCnt(res.RowsAffected()) {
		mr.Configuration = append(mr.Configuration, q.Configuration)
	}
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
