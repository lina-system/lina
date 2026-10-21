// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package main

func platformCanary() error { return nil }
