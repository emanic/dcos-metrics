// Copyright 2016 Mesosphere, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/dcos/dcos-go/dcos"
	"github.com/dcos/dcos-metrics/producers"
	"github.com/urfave/cli"
)

type Plugin struct {
	App             *cli.App
	Name            string
	Endpoints       []string
	Role            string
	PollingInterval int
	MetricsPort     string
	MetricsProto    string
	MetricsHost     string
	AuthToken       string
	Log             *logrus.Entry
	ConnectorFunc   func([]producers.MetricsMessage, *cli.Context) error
}

var VERSION = "UNSET"

// New returns a mandatory plugin config which every plugin for
// metrics will need
func New(options ...Option) (*Plugin, error) {
	newPlugin := &Plugin{
		Name:            "default",
		Role:            "",
		PollingInterval: 10,
		MetricsProto:    "http",
		MetricsHost:     "localhost",
		MetricsPort:     "61001",
		AuthToken:       "",
	}

	newPlugin.App = cli.NewApp()
	newPlugin.App.Version = VERSION
	newPlugin.App.Flags = []cli.Flag{
		cli.StringFlag{
			Name:        "metrics-host",
			Value:       newPlugin.MetricsHost,
			Usage:       "The IP or hostname where DC/OS metrics is running",
			Destination: &newPlugin.MetricsHost,
		},
		cli.StringFlag{
			Name:        "metrics-proto",
			Value:       newPlugin.MetricsProto,
			Usage:       "The HTTP protocol for the DC/OS metrics service",
			Destination: &newPlugin.MetricsProto,
		},
		cli.StringFlag{
			Name:        "metrics-port",
			Value:       newPlugin.MetricsPort,
			Usage:       "Port the DC/OS metrics service is running.Defaults to agent adminrouter port",
			Destination: &newPlugin.MetricsPort,
		},
		cli.IntFlag{
			Name:        "polling-interval",
			Value:       newPlugin.PollingInterval,
			Usage:       "Polling interval for metrics in seconds",
			Destination: &newPlugin.PollingInterval,
		},

		cli.StringFlag{
			Name:        "auth-token",
			Value:       newPlugin.AuthToken,
			Usage:       "Valid authentication token for DC/OS services",
			Destination: &newPlugin.AuthToken,
		},
		cli.StringFlag{
			Name:        "dcos-role",
			Value:       newPlugin.Role,
			Usage:       "DC/OS role, either master or agent",
			Destination: &newPlugin.Role,
		},
	}

	for _, o := range options {
		if err := o(newPlugin); err != nil {
			return newPlugin, err
		}
	}

	newPlugin.Log = logrus.WithFields(logrus.Fields{"plugin": newPlugin.Name})

	return newPlugin, nil
}

func (p *Plugin) StartPlugin() error {
	p.App.Action = func(c *cli.Context) error {
		for {
			metrics, err := p.Metrics()
			if err != nil {
				return err
			}

			if err := p.ConnectorFunc(metrics, c); err != nil {
				return err
			}

			p.Log.Infof("Polling complete, sleeping for %d seconds", p.PollingInterval)
			time.Sleep(time.Duration(p.PollingInterval) * time.Second)
		}

		return nil
	}

	return p.App.Run(os.Args)
}

func (p *Plugin) Metrics() ([]producers.MetricsMessage, error) {
	p.Log.Info("Getting metrics from metrics service")
	metricsMessages := []producers.MetricsMessage{}

	if err := p.setEndpoints(); err != nil {
		p.Log.Fatal(err)
	}

	for _, path := range p.Endpoints {
		metricsURL := url.URL{
			Scheme: p.MetricsProto,
			Host:   net.JoinHostPort(p.MetricsHost, p.MetricsPort),
			Path:   path,
		}

		if len(p.AuthToken) == 0 {
			return metricsMessages, errors.New("Auth token must be set, use --auth-token <token>")
		}

		request := &http.Request{
			Method: "GET",
			URL:    &metricsURL,
			Header: http.Header{
				"Authorization": []string{fmt.Sprintf("token=%s", p.AuthToken)},
			},
		}

		metricMessage, err := makeMetricsRequest(request)
		if err != nil {
			return metricsMessages, err
		}

		metricsMessages = append(metricsMessages, metricMessage)
		p.Log.Infof("Received data from metrics service endpoint %s, success!", request.URL.Path)
	}

	return metricsMessages, nil
}

// SetEndpoints uses the role passed as a flag to generate the metrics endpoints
// this instance should use.
func (p *Plugin) setEndpoints() error {
	p.Log.Infof("Setting plugin endpoints for role %s", p.Role)
	if p.Role == dcos.RoleMaster {
		p.Endpoints = []string{
			"/system/v1/metrics/v0/node",
		}
		return nil
	}

	if p.Role == dcos.RoleAgent || p.Role == dcos.RoleAgentPublic {
		p.Endpoints = []string{
			"/system/v1/metrics/v0/node",
		}

		containers := []string{}
		metricsURL := url.URL{
			Scheme: p.MetricsProto,
			Host:   net.JoinHostPort(p.MetricsHost, p.MetricsPort),
			Path:   "/system/v1/metrics/v0/containers",
		}

		request := &http.Request{
			Method: "GET",
			URL:    &metricsURL,
			Header: http.Header{
				"Authorization": []string{fmt.Sprintf("token=%s", p.AuthToken)},
			},
		}

		client := &http.Client{}
		resp, err := client.Do(request)
		if err != nil {
			return err
		}

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			p.Log.Errorf("Encountered error reading response body, %s", err.Error())
			return err
		}

		if err := json.Unmarshal(body, &containers); err != nil {
			return err
		}

		for _, c := range containers {
			e := "/system/v1/metrics/v0/containers/" + c
			p.Log.Infof("Discovered new container endpoint %s", e)
			p.Endpoints = append(p.Endpoints, e)
		}

		return nil
	}

	return errors.New("Role must be either 'master' or 'agent'")
}

/*** Helpers ***/
func makeMetricsRequest(request *http.Request) (producers.MetricsMessage, error) {
	l := logrus.WithFields(logrus.Fields{"plugin": "http-helper"})

	l.Infof("Making request to %+v", request.URL)
	client := &http.Client{}
	mm := producers.MetricsMessage{}

	resp, err := client.Do(request)
	if err != nil {
		l.Errorf("Encountered error requesting data, %s", err.Error())
		return mm, err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		l.Errorf("Encountered error reading response body, %s", err.Error())
		return mm, err
	}

	err = json.Unmarshal(body, &mm)
	if err != nil {
		l.Errorf("Encountered error parsing JSON, %s. JSON Content was: %s", err.Error(), body)
		return mm, err
	}

	return mm, nil
}
