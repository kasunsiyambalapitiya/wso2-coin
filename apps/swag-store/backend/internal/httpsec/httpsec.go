// Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

// Package httpsec guards the base URLs of upstream services: every one must be
// https, except a loopback host used for local fakes.
package httpsec

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// RequireSecureURL returns an error unless raw is an absolute https URL, or an
// http URL whose host is loopback. Credentials in the URL are refused, because
// they would end up in logs.
func RequireSecureURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("URL %q has no host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("URL must not carry credentials")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("URL %q must use https (http is allowed for loopback only)", raw)
	default:
		return fmt.Errorf("URL %q must use https", raw)
	}
}

// IsLoopbackURL reports whether raw parses and points at a loopback host.
func IsLoopbackURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
