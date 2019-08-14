/*-
 * Copyright (c) 2018, 1&1 Internet SE
 * All rights reserved
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package rest // import "github.com/solnx/eye/internal/eye.rest"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/go-resty/resty"
	"github.com/julienschmidt/httprouter"
	"github.com/mjolnir42/soma/lib/proto"
	uuid "github.com/satori/go.uuid"
	msg "github.com/solnx/eye/internal/eye.msg"
)

// DeploymentNotification implements the API call that receives
// push notifications from SOMA
func (x *Rest) DeploymentNotification(w http.ResponseWriter, r *http.Request,
	params httprouter.Params) {
	defer panicCatcher(w)
	// craft internal request message
	request := msg.New(r, params)
	request.Section = msg.SectionDeployment
	request.Action = msg.ActionNotification
	// decode client payload
	clientReq := proto.NewPushNotification()
	if err := decodeJSONBody(r, &clientReq); err != nil {
		x.appLog.Errorf("Section=%s Action=%s Error=%s", request.Section, request.Action, err.Error())
		x.replyUnprocessableEntity(&w, &request, err)
		return
	}

	// validate client payload
	govalidator.SetFieldsRequiredByDefault(false)
	govalidator.TagMap[`abspath`] = govalidator.Validator(func(str string) bool {
		return filepath.IsAbs(str)
	})
	if ok, err := govalidator.ValidateStruct(clientReq); !ok {
		x.appLog.Errorf("Section=%s Action=%s Error=%s", request.Section, request.Action, err.Error())
		x.replyUnprocessableEntity(&w, &request, err)
		return
	}

	request.Notification = struct {
		ID         uuid.UUID
		PathPrefix string
	}{
		ID:         uuid.FromStringOrNil(clientReq.UUID),
		PathPrefix: clientReq.Path,
	}

	if uuid.Equal(request.Notification.ID, uuid.Nil) {
		x.replyBadRequest(&w, &request, nil)
		return
	}

	// request authorization for request
	if !x.isAuthorized(&request) {
		x.replyForbidden(&w, &request, nil)
		return
	}

	// fetch DeploymentDetails for ID
	x.fetchPushDeployment(&w, &request)
}

// DeploymentProcess accepts SOMA deployment results
func (x *Rest) DeploymentProcess(w http.ResponseWriter, r *http.Request,
	params httprouter.Params) {
	defer panicCatcher(w)
	request := msg.New(r, params)
	request.Section = msg.SectionDeployment
	request.Action = msg.ActionProcess

	var err error
	cReq := proto.NewDeploymentResult()
	if err = decodeJSONBody(r, &cReq); err != nil {
		x.appLog.Errorf("Section=%s Action=%s Error=%s", request.Section, request.Action, err.Error())
		x.replyUnprocessableEntity(&w, &request, err)
		return
	}

	if err = resolveFlags(nil, &request); err != nil {
		x.appLog.Errorf("Section=%s Action=%s Error=%s", request.Section, request.Action, err.Error())
		x.replyBadRequest(&w, &request, err)
		return
	}

	if len(*cReq.Deployments) != 1 {
		x.appLog.Errorf("Section=%s Action=%s Error=%s", request.Section, request.Action, fmt.Errorf("Deployment count %d != 1", len(*cReq.Deployments)))
		x.replyUnprocessableEntity(&w, &request, fmt.Errorf("Deployment count %d != 1", len(*cReq.Deployments)))
		return
	}

	request.ConfigurationTask = (*cReq.Deployments)[0].Task
	if request.LookupHash, request.Configuration, err = processDeploymentDetails(&(*cReq.Deployments)[0]); err != nil {
		x.appLog.Errorf("Section=%s Action=%s Error=%s", request.Section, request.Action, err.Error())
		x.replyInternalError(&w, &request, err)
		return
	}

	// build URL to send deployment feedback
	x.somaSetFeedbackURL(&request)

	switch r.Method {
	// called via v1 update API PUT:/api/v1/item/:ID
	case `PUT`:
		if request.Configuration.ID != params.ByName(`ID`) {
			x.appLog.Debugf("Section=%s Action=%s Error=%s", request.Section, request.Action, fmt.Errorf(
				"Mismatched IDs in update: [%s] vs [%s]",
				request.Configuration.ID,
				params.ByName(`ID`),
			))
			x.replyBadRequest(&w, &request, fmt.Errorf(
				"Mismatched IDs in update: [%s] vs [%s]",
				request.Configuration.ID,
				params.ByName(`ID`),
			))
			return
		}

		// v1 PUT API returned an error if the deployment was not
		// a rollout
		if request.ConfigurationTask != msg.TaskRollout {
			x.appLog.Debugf("Section=%s Action=%s Error=%s", request.Section, request.Action, fmt.Errorf(
				"Update for ID %s is not a rollout (%s)",
				params.ByName(`ID`),
				request.ConfigurationTask,
			))
			x.replyBadRequest(&w, &request, fmt.Errorf(
				"Update for ID %s is not a rollout (%s)",
				params.ByName(`ID`),
				request.ConfigurationTask,
			))
			return
		}
	case `POST`:
		if r.URL.EscapedPath() == `/api/v1/item/` {
			// v1 POST API returned an error if the deployment was not
			// a rollout
			if request.ConfigurationTask != msg.TaskRollout {
				x.appLog.Debugf("Section=%s Action=%s Error=%s", request.Section, request.Action, fmt.Errorf(
					"Update for ID %s is not a rollout (%s)",
					params.ByName(`ID`),
					request.ConfigurationTask,
				))
				x.replyBadRequest(&w, &request, fmt.Errorf(
					"Update for ID %s is not a rollout (%s)",
					params.ByName(`ID`),
					request.ConfigurationTask,
				))
				return
			}
		}
	}

	if !x.isAuthorized(&request) {
		x.replyForbidden(&w, &request, nil)
		return
	}

	handler := x.handlerMap.Get(`deployment_w`)
	handler.Intake() <- request
	result := <-request.Reply
	x.respond(&w, &result)
}

// fetchPushDeployment fetches DeploymentDetails for which a push
// notification was received by x.DeploymentNotification
func (x *Rest) fetchPushDeployment(w *http.ResponseWriter, q *msg.Request) {
	var (
		resp *resty.Response
		res  proto.Result
		err  error
	)
	// build URL to download deploymentDetails
	soma, _ := url.Parse(x.conf.Eye.SomaURL)
	soma.Path = fmt.Sprintf("%s/%s",
		q.Notification.PathPrefix,
		q.Notification.ID.String(),
	)

	foldSlashes(soma)
	detailsDownload := soma.String()
	// fetch DeploymentDetails inside concurrency limited go routine
	// without blocking the full handler within the limiter

	resp, err = resty.New().
		// set generic client options
		SetDisableWarn(true).
		SetHeader(`Content-Type`, `application/json`).
		SetContentLength(true).
		// follow redirects
		SetRedirectPolicy(resty.FlexibleRedirectPolicy(5)).
		// configure request retry
		SetRetryCount(x.conf.Eye.RetryCount).
		SetRetryWaitTime(time.Duration(x.conf.Eye.RetryMinWaitTime) * time.Millisecond).
		SetRetryMaxWaitTime(time.Duration(x.conf.Eye.RetryMaxWaitTime) * time.Millisecond).
		// reset timeout deadline before every request
		OnBeforeRequest(func(cl *resty.Client, rq *resty.Request) error {
			cl.SetTimeout(time.Duration(x.conf.Eye.RequestTimeout) * time.Millisecond)
			return nil
		}).
		// enter concurrency limit before performing request
		OnBeforeRequest(func(cl *resty.Client, rq *resty.Request) error {
			x.limit.Start()
			return nil
		}).
		// leave concurrency limit after receiving a response
		OnAfterResponse(func(cl *resty.Client, rp *resty.Response) error {
			x.limit.Done()
			return nil
		}).
		// clear timeout deadline after each request (http.Client
		// timeout also cancels reading the response body)
		OnAfterResponse(func(cl *resty.Client, rp *resty.Response) error {
			cl.SetTimeout(0)
			return nil
		}).
		R().Get(detailsDownload)

	// block on running go routine

	if err != nil {
		x.appLog.Errorf("Section=%s Action=%s Error=%s", "Deployment", "fetchPushDeployment", err.Error())
		x.replyGatewayTimeout(w, q, err)
		return
	}

	// HTTP protocol statuscode > 299
	if resp.StatusCode() > 299 {
		x.appLog.Errorf("Received: %d/%s", resp.StatusCode(), resp.Status())
		x.replyBadGateway(w, q, fmt.Errorf("Received: %d/%s", resp.StatusCode(), resp.Status()))
		return
	}
	if err = json.Unmarshal(resp.Body(), &res); err != nil {
		x.appLog.Errorln("Could not unmarshal the details: ", err.Error())
		x.replyUnprocessableEntity(w, q, err)
		return
	}

	// SOMA application statuscode != 200
	if res.StatusCode != 200 {
		x.appLog.Errorf("SOMA: %d/%s", res.StatusCode, res.StatusText)
		x.replyGone(w, q, fmt.Errorf("SOMA: %d/%s", res.StatusCode, res.StatusText))
		return
	}

	if len(*res.Deployments) != 1 {
		x.appLog.Errorf("Deployment count %d != 1", len(*res.Deployments))
		x.replyUnprocessableEntity(w, q, fmt.Errorf("Deployment count %d != 1", len(*res.Deployments)))
		return
	}

	q.ConfigurationTask = (*res.Deployments)[0].Task
	if q.LookupHash, q.Configuration, err = processDeploymentDetails(&(*res.Deployments)[0]); err != nil {
		x.appLog.Errorf("Section=%s Action=%s Error=%s", "Deployment", "fetchPushDeployment", err.Error())
		x.replyInternalError(w, q, err)
		return
	}
	x.appLog.Infoln("Received deployment notification for lookup hash: ", q.LookupHash, "Task:", q.ConfigurationTask)
	if err := resolveFlags(nil, q); err != nil {
		x.appLog.Errorf("Section=%s Action=%s Error=%s", "Deployment", "fetchPushDeployment", err.Error())
		x.replyBadRequest(w, q, err)
		return
	}
	// build URL to send deployment feedback
	x.somaSetFeedbackURL(q)
	if !x.isAuthorized(q) {
		x.replyForbidden(w, q, nil)
		return
	}
	handler := x.handlerMap.Get(`deployment_w`)
	handler.Intake() <- *q
	result := <-q.Reply
	x.respond(w, &result)
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
