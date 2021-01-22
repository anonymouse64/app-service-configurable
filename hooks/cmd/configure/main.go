// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2017 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package main

import (
	"encoding/json"
	"fmt"
	"log/syslog"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/edgex-go/app-service-configurable/hooks"
)

var log syslog.Writer
var snapData string = os.Getenv("SNAP_DATA")

// Client is the base struct for runtime and testing
type Client struct {
	getter Getter
}

// Get is the test obj for overridding functions
type Get struct{}

// Getter interface is for overriding SnapGet for testing
type Getter interface {
	SnapGet(string) (string, error)
}

// GetClient returns a normal runtime client
func GetClient() *Client {
	return &Client{getter: &Get{}}
}

// ModClient returns a testing client
func ModClient(g Getter) *Client {
	return &Client{getter: g}
}

// SnapGet uses snapctrl to get a value from a key, or returns error
func (g *Get) SnapGet(key string) (string, error) {
	out, err := exec.Command("snapctl", "get", key).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// snapGetStr wraps SnapGet for string types and verifies the snap var is valid
func (c *Client) snapGetStr(key string, target *string) {
	val, err := c.getter.SnapGet(key)
	if err != nil {
		return
	}
	if len(val) == 0 {
		log.Err("configure error: key " + key + " exists but has zero length")
		return
	}
	*target = val
}

func (c *Client) snapGetInt(key string, target *int) {
	val, err := c.getter.SnapGet(key)
	if err != nil {
		return
	}

	if len(val) == 0 {
		return
	}

	*target, err = strconv.Atoi(val)
	if err != nil {
		log.Err("bad integer: " + err.Error())
		*target = 0
	}
}

// snapGetBool wraps SnapGet for bool types and verifies the snap var is valid
func (c *Client) snapGetBool(key string, target *bool) {
	val, err := c.getter.SnapGet(key)
	if err != nil {
		return
	}
	if len(val) == 0 {
		log.Err("configure error: key " + key + " exists but has zero length")
		return
	}

	if val == "true" {
		*target = true
	} else {
		*target = false
	}
}

// it may return false on e.g. permission issues.
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func handleVal(p string, k string, v interface{}, flatM map[string]interface{}) {
	var mk string

	// top level keys don't include "env", so no separator needed
	if p == "" {
		mk = k
	} else {
		mk = fmt.Sprintf("%s.%s", p, k)
	}

	log.Info(fmt.Sprintf("handleVal: mk: %s", mk))

	switch t := v.(type) {
	case string:
		log.Info(fmt.Sprintf("ADDING %s=%s to flatM", k, t))
		flatM[mk] = t
	case bool:
		log.Info(fmt.Sprintf("ADDING %s=%v to flatM", k, t))
		flatM[mk] = strconv.FormatBool(t)
	case float64:
		log.Info(fmt.Sprintf("ADDING %s=%v to flatM", k, t))
		flatM[mk] = strconv.FormatFloat(t, 'f', -1, 64)
	case map[string]interface{}:
		log.Info(fmt.Sprintf("FOUND AN OBJECT"))

		for k, v := range t {
			handleVal(mk, k, v, flatM)
		}
	default:
		log.Err("I DON'T KNOW!!!!")
	}
}

func handleSvcConf(env string) {
	log.Info(fmt.Sprintf("edgex-asc:configure:handleSvcConf config is %s", env))

	if env == "" {
		return
	}

	var m map[string]interface{}
	var flatM map[string]interface{}
	flatM = make(map[string]interface{})

	err := json.Unmarshal([]byte(env), &m)
	if err != nil {
		log.Err(fmt.Sprintf("edgex-asc:configure:handleSvcConf: failed to unmarshall env; %v", err))
		return
	}

	for k, v := range m {
		handleVal("", k, v, flatM)
	}

	path := fmt.Sprintf("%s/config/res/service.env", snapData)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		log.Err(fmt.Sprintf("edgex-asc:configure:handleSvcConf: can't open %s - %v", path, err))
		os.Exit(1)
	}

	defer f.Close()

	log.Info(fmt.Sprintf("edgex-asc:configure:handleSvcConf about write %s", path))
	for k, v := range flatM {
		log.Info(fmt.Sprintf("%s=%v", k, v))
		_, err := f.WriteString(fmt.Sprintf("export %s=%s\n", hooks.ConfToEnv[k], v))
		if err != nil {
			log.Err(fmt.Sprintf("edgex-asc:configure:handleSvcConf: can't open %s - %v", path, err))
			os.Exit(1)
		}
	}
}

func handleProf(prof string) {
	log.Info(fmt.Sprintf("edgex-asc:configure:handleProf: profile is %s", prof))

	if prof == "" || prof == "default" {
		return
	}

	path := fmt.Sprintf("%s/config/res/%s/configuration.toml", snapData, prof)
	log.Info(fmt.Sprintf("edgex-asc:configure:handleProf: checking if %s exists", path))

	_, err := os.Stat(path)
	if err != nil {
		log.Err(fmt.Sprintf("edgex-asc:configure:handleProf: invalid setting profile %s specified; no configuration.toml found", prof))
		os.Exit(1)
	}

	log.Info(fmt.Sprintf("edgex-asc:configure:handleProf: OK!!!"))
}

func main() {
	var env, prof string

	log, err := syslog.New(syslog.LOG_INFO, "edgex-asc:configure")
	if err != nil {
		return
	}

	if snapData == "" {
		log.Crit("edgex-asc:configure: SNAP_DATA not set!")
		os.Exit(1)
	}

	// TODO: remove DEBUG code
	for k, v := range hooks.ConfToEnv {
		log.Info(fmt.Sprintf("%s=%v", k, v))
	}

	log.Info("edgex-asc:configure hook running")
	client := GetClient()

	client.snapGetStr("profile", &prof)
	handleProf(prof)

	client.snapGetStr("env", &env)
	handleSvcConf(env)
}
