// SPDX-License-Identifier: Apache-2.0

// Package miniapps hosts the small web apps LINA builds for a person. Each app
// has its own origin, a token limited to its declared reads, requests and hosts,
// and a numbered history of the files a change produced.
package miniapps

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalid     = errors.New("mini_app_invalid")
	ErrNotFound    = errors.New("mini_app_not_found")
	ErrForbidden   = errors.New("mini_app_forbidden")
	ErrStale       = errors.New("mini_app_stale")
	ErrCheck       = errors.New("mini_app_check_failed")
	ErrUnavailable = errors.New("mini_app_unavailable")
)

var (
	appIDPattern    = regexp.MustCompile(`^[0-9a-f]{16}$`)
	readPattern     = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,63}$`)
	requestPattern  = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,63}$`)
	hostPattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	schedulePattern = regexp.MustCompile(`^([1-9][0-9]{0,3})(m|h)$`)
)

// Manifest is everything the person allows before the first open. Reads and
// requests are Core names; hosts are the only addresses Core may fetch for the
// app. A schedule runs the owner agent against the app's store.
type Manifest struct {
	Name     string   `json:"name"`
	Reads    []string `json:"reads,omitempty"`
	Requests []string `json:"requests,omitempty"`
	Hosts    []string `json:"hosts,omitempty"`
	Schedule string   `json:"schedule,omitempty"`
}

func ValidateManifest(m Manifest) error {
	if strings.TrimSpace(m.Name) == "" || utf8.RuneCountInString(m.Name) > 80 || strings.IndexFunc(m.Name, unicode.IsControl) >= 0 {
		return ErrInvalid
	}
	valid := func(values []string, pattern *regexp.Regexp, max int) error {
		if len(values) > max {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for _, value := range values {
			if !pattern.MatchString(value) || seen[value] {
				return ErrInvalid
			}
			seen[value] = true
		}
		return nil
	}
	if err := valid(m.Reads, readPattern, 32); err != nil {
		return err
	}
	if err := valid(m.Requests, requestPattern, 32); err != nil {
		return err
	}
	if len(m.Hosts) > 32 {
		return ErrInvalid
	}
	for _, host := range m.Hosts {
		if !hostPattern.MatchString(host) || strings.Contains(host, "..") {
			return ErrInvalid
		}
		if u, err := url.Parse("http://" + host); err != nil || u.Host != host || u.Path != "" {
			return ErrInvalid
		}
	}
	if m.Schedule != "" && !schedulePattern.MatchString(m.Schedule) {
		return ErrInvalid
	}
	return nil
}

// Widens reports whether next adds a read, request or host, or changes the
// schedule. Those changes need the person's allow decision again.
func (m Manifest) Widens(next Manifest) bool {
	for _, value := range next.Reads {
		if !slices.Contains(m.Reads, value) {
			return true
		}
	}
	for _, value := range next.Requests {
		if !slices.Contains(m.Requests, value) {
			return true
		}
	}
	for _, value := range next.Hosts {
		if !slices.Contains(m.Hosts, value) {
			return true
		}
	}
	return m.Schedule != next.Schedule
}

// Narrows reports whether next drops a read, request or host. Narrowing revokes
// the app's token so an open page cannot keep using the old scope.
func (m Manifest) Narrows(next Manifest) bool {
	drops := func(old, now []string) bool {
		for _, value := range old {
			if !slices.Contains(now, value) {
				return true
			}
		}
		return false
	}
	return drops(m.Reads, next.Reads) || drops(m.Requests, next.Requests) || drops(m.Hosts, next.Hosts) || m.Schedule != next.Schedule
}

func (m Manifest) HasRead(name string) bool    { return slices.Contains(m.Reads, name) }
func (m Manifest) HasRequest(name string) bool { return slices.Contains(m.Requests, name) }

// HasHost matches the URL's host, lowercased and without a port.
func (m Manifest) HasHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return slices.Contains(m.Hosts, host)
}

func (m Manifest) String() string {
	parts := []string{"reads " + strings.Join(m.Reads, ", "), "requests " + strings.Join(m.Requests, ", "), "hosts " + strings.Join(m.Hosts, ", ")}
	if m.Schedule != "" {
		parts = append(parts, "schedule "+m.Schedule)
	}
	return fmt.Sprintf("%s: %s", m.Name, strings.Join(parts, "; "))
}

func cloneManifest(m Manifest) Manifest {
	m.Reads = slices.Clone(m.Reads)
	m.Requests = slices.Clone(m.Requests)
	m.Hosts = slices.Clone(m.Hosts)
	return m
}
