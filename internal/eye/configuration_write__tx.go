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
	"github.com/lib/pq"
	msg "github.com/solnx/eye/internal/eye.msg"
	"github.com/solnx/eye/lib/eye.proto/v2"
)

func init() {
	v2.PosTimeInf = msg.PosTimeInf
	v2.NegTimeInf = msg.NegTimeInf
	v2.TimeFormatString = RFC3339Milli
}

// txCfgLoadActive checks within the provided transaction tx if the
// configuration referenced by q.Configuration.ID is currently active.
// If no such active configuration exists, it returns sql.ErrNoRows. If
// a matching configuration is found, it is populated into cfg.
func (w *ConfigurationWrite) txCfgLoadActive(tx *sql.Tx, q *msg.Request,
	cfg *v2.Configuration, logger *logrus.Logger) (err error) {

	var (
		dataID, confResult         string
		tasks                      []string
		provisionTS, deprovisionTS time.Time
		validFrom, validUntil      time.Time
		activatedAt                time.Time
		data                       v2.Data
	)
	// database index ensures there is no overlap in validity ranges
	if err = tx.Stmt(w.stmtCfgSelectValidForUpdate).QueryRow(
		q.Configuration.ID,
	).Scan(
		&dataID,
		&validFrom,
	); err != nil {
		cfg = nil
		return
	}

	// load full current configuration
	if err = tx.Stmt(w.stmtCfgShow).QueryRow(
		dataID,
	).Scan(
		&confResult,
		&validUntil,
		&provisionTS,
		&deprovisionTS,
		pq.Array(&tasks),
	); err == sql.ErrNoRows {
		// sql.ErrNoRows == row disappeared mid-transaction
		err = fmt.Errorf(
			"Entry for ConfigurationID %s, DataID %s"+
				" disappeared mid-transaction",
			q.Configuration.ID,
			dataID,
		)
		cfg = nil
		return
	} else if err != nil {
		logger.Errorln(q.Configuration.ID, "txCfgLoadActive: stmtCfgShow err:", err.Error())
		cfg = nil
		return
	}

	// unmarshal stored configuration
	if err = json.Unmarshal([]byte(confResult), cfg); err != nil {
		cfg = nil
		return
	}

	// query if this configurationID is activated
	if err = tx.Stmt(w.stmtActivationGet).QueryRow(
		q.Configuration.ID,
	).Scan(
		&activatedAt,
	); err == sql.ErrNoRows {
		cfg.ActivatedAt = `never`
	} else if err != nil && err != sql.ErrNoRows {
		logger.Errorln(q.Configuration.ID, "txCfgLoadActive: stmtActivationGet err:", err.Error())
		cfg = nil
		return
	} else {
		cfg.ActivatedAt = activatedAt.Format(RFC3339Milli)
	}

	// populate result metadata
	data = cfg.Data[0]
	data.Info = v2.MetaInformation{
		ValidFrom:       v2.FormatValidity(validFrom),
		ValidUntil:      v2.FormatValidity(validUntil),
		ProvisionedAt:   v2.FormatProvision(provisionTS),
		DeprovisionedAt: v2.FormatProvision(deprovisionTS),
		Tasks:           tasks,
	}
	cfg.Data = []v2.Data{data}
	return
}

// txInsertCfgData adds data for configurationID and starts a
// provisioning period
func (w *ConfigurationWrite) txInsertCfgData(tx *sql.Tx, mr *msg.Result,
	dataID, configurationID string, from time.Time, data []byte) (ok bool, err error) {

	var res sql.Result

	if res, err = tx.Stmt(w.stmtCfgAddData).Exec(
		dataID,
		configurationID,
		from,
		data,
	); err != nil {
		ok = false
		return
	}
	if !mr.ExpectedRows(&res, 1) {
		ok = false
		return
	}
	ok, err = w.txStartProvision(tx, mr, from, dataID, configurationID)
	return
}

// txSetDataValidity updates the the validity of dataID
func (w *ConfigurationWrite) txSetDataValidity(tx *sql.Tx, mr *msg.Result,
	from, until time.Time, dataID string) (ok bool, err error) {

	var res sql.Result
	ok = true

	if res, err = tx.Stmt(w.stmtCfgDataUpdateValidity).Exec(
		from,
		until,
		dataID,
	); err != nil {
		ok = false
		return
	}
	if !mr.ExpectedRows(&res, 1) {
		ok = false
		return
	}
	return
}

// txStartProvision starts a provisioning period for dataID
func (w *ConfigurationWrite) txStartProvision(tx *sql.Tx, mr *msg.Result,
	from time.Time, dataID, configurationID string) (ok bool, err error) {

	var res sql.Result
	ok = true

	if res, err = tx.Stmt(w.stmtProvAdd).Exec(
		dataID,
		configurationID,
		from,
		pq.Array([]string{msg.TaskRollout}),
	); err != nil {
		ok = false
		return
	}
	if !mr.ExpectedRows(&res, 1) {
		ok = false
		return
	}
	return
}

// txFinalizeProvision closes the provisioning period for dataID
func (w *ConfigurationWrite) txFinalizeProvision(tx *sql.Tx, mr *msg.Result,
	until time.Time, dataID, task string) (ok bool, err error) {

	var res sql.Result
	ok = true

	if res, err = tx.Stmt(w.stmtProvFinalize).Exec(
		dataID,
		until,
		task,
	); err != nil {
		ok = false
		return
	}
	if !mr.ExpectedRows(&res, 1) {
		ok = false
		return
	}
	return
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
